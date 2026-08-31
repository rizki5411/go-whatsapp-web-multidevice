package whatsapp

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainMessageQueue "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/messagequeue"
	"github.com/sirupsen/logrus"
)

// Per-device outbound send queue worker.
//
// Shaped like StartPresencePulseScheduler: one process-wide supervisor goroutine
// that re-reads the device registry every tick and fans out a short-lived
// goroutine per due device, gated on the device being connected and logged in.
// That gives the per-device lifecycle for free — a device that connects is
// picked up within one tick, a device that drops is simply skipped — without any
// connect/disconnect hook, and it makes restart recovery implicit: rows left
// pending in the database are found by the next tick.
//
// Sends go back through the existing send usecase via the injected dispatcher.
// This package cannot import usecase (usecase imports this package), so both the
// repository and the dispatcher arrive as an interface and a func value wired up
// in cmd.
const (
	messageQueueDefaultCheckInterval = 15 * time.Second
	messageQueueDefaultMinDelay      = 2 * time.Minute
	messageQueueDefaultMaxDelay      = 5 * time.Minute
	// messageQueueSendTimeout bounds one dispatch so a stuck upload cannot pin a
	// device's queue forever.
	messageQueueSendTimeout = 5 * time.Minute
)

// MessageQueueDispatcher delivers one queued row and returns the WhatsApp
// message id. Implemented in the usecase layer, which owns the send logic.
type MessageQueueDispatcher func(ctx context.Context, msg *domainMessageQueue.QueuedMessage) (messageID string, err error)

// messageQueueDevice is the worker's view of a device: an id, a readiness check,
// and a way to build the device-scoped context the send usecase expects. Kept as
// func fields rather than a *DeviceInstance so tests can supply fakes.
type messageQueueDevice struct {
	id         string
	ready      func() bool
	withDevice func(ctx context.Context) context.Context
}

type messageQueueDeviceSource interface {
	ListMessageQueueDevices() []messageQueueDevice
}

type deviceManagerMessageQueueSource struct {
	manager *DeviceManager
}

func (s deviceManagerMessageQueueSource) ListMessageQueueDevices() []messageQueueDevice {
	if s.manager == nil {
		return nil
	}

	instances := s.manager.ListDevices()
	devices := make([]messageQueueDevice, 0, len(instances))
	for _, instance := range instances {
		if instance == nil || instance.ID() == "" {
			continue
		}
		inst := instance
		devices = append(devices, messageQueueDevice{
			id:    inst.ID(),
			ready: func() bool { return inst.IsConnected() && inst.IsLoggedIn() },
			withDevice: func(ctx context.Context) context.Context {
				return ContextWithDevice(ctx, inst)
			},
		})
	}
	return devices
}

type messageQueueScheduler struct {
	source        messageQueueDeviceSource
	repo          domainMessageQueue.IMessageQueueRepository
	dispatch      MessageQueueDispatcher
	minDelay      time.Duration
	maxDelay      time.Duration
	checkInterval time.Duration
	sendTimeout   time.Duration
	now           func() time.Time
	randDelay     func(min, max time.Duration) time.Duration

	mu sync.Mutex
	// nextEligible is the earliest time a device may send again. This is what
	// enforces the random spacing, and it is per device so ten devices drain
	// their queues independently.
	nextEligible map[string]time.Time
	inFlight     map[string]bool
	// seeded marks devices whose spacing has been primed from the database, so a
	// restart does not immediately fire off a message the old process just sent.
	seeded map[string]bool
}

func newMessageQueueScheduler(
	source messageQueueDeviceSource,
	repo domainMessageQueue.IMessageQueueRepository,
	dispatch MessageQueueDispatcher,
	minDelay, maxDelay, checkInterval time.Duration,
) *messageQueueScheduler {
	if minDelay <= 0 {
		minDelay = messageQueueDefaultMinDelay
	}
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	if checkInterval <= 0 {
		checkInterval = messageQueueDefaultCheckInterval
	}

	return &messageQueueScheduler{
		source:        source,
		repo:          repo,
		dispatch:      dispatch,
		minDelay:      minDelay,
		maxDelay:      maxDelay,
		checkInterval: checkInterval,
		sendTimeout:   messageQueueSendTimeout,
		now:           time.Now,
		randDelay:     randomQueueDelay,
		nextEligible:  make(map[string]time.Time),
		inFlight:      make(map[string]bool),
		seeded:        make(map[string]bool),
	}
}

// StartMessageQueueScheduler launches the supervisor. Safe to call with a nil
// manager, repo or dispatcher: it logs and does nothing.
func StartMessageQueueScheduler(
	ctx context.Context,
	manager *DeviceManager,
	repo domainMessageQueue.IMessageQueueRepository,
	dispatch MessageQueueDispatcher,
	minDelay, maxDelay, checkInterval time.Duration,
) {
	if manager == nil {
		logrus.Warn("[MESSAGE_QUEUE] device manager is nil; scheduler not started")
		return
	}
	if repo == nil {
		logrus.Warn("[MESSAGE_QUEUE] queue repository is nil; scheduler not started")
		return
	}
	if dispatch == nil {
		logrus.Warn("[MESSAGE_QUEUE] dispatcher is nil; scheduler not started")
		return
	}

	scheduler := newMessageQueueScheduler(
		deviceManagerMessageQueueSource{manager: manager},
		repo,
		dispatch,
		minDelay,
		maxDelay,
		checkInterval,
	)
	go scheduler.run(ctx)
}

func (s *messageQueueScheduler) run(ctx context.Context) {
	if s == nil {
		return
	}

	s.reconcile()
	s.processDueDevices(ctx)

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.processDueDevices(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// reconcile runs once at startup: close out rows a crash left mid-send and
// reclaim media files nothing references any more.
func (s *messageQueueScheduler) reconcile() {
	if s == nil || s.repo == nil {
		return
	}

	if count, err := s.repo.ResetInterruptedSending(); err != nil {
		logrus.WithError(err).Warn("[MESSAGE_QUEUE] failed to close out interrupted sends")
	} else if count > 0 {
		// Deliberately not retried: WhatsApp may already have accepted these, and
		// a duplicate message is worse than a missing one.
		logrus.Warnf("[MESSAGE_QUEUE] marked %d send(s) interrupted by restart as failed", count)
	}

	s.sweepOrphanMedia()
}

func (s *messageQueueScheduler) sweepOrphanMedia() {
	paths, err := s.repo.ListPendingMediaPaths()
	if err != nil {
		logrus.WithError(err).Warn("[MESSAGE_QUEUE] failed to list queued media; skipping sweep")
		return
	}

	keep := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if abs, absErr := filepath.Abs(path); absErr == nil {
			keep[abs] = struct{}{}
		}
	}

	entries, err := os.ReadDir(config.PathMessageQueue)
	if err != nil {
		if !os.IsNotExist(err) {
			logrus.WithError(err).Warn("[MESSAGE_QUEUE] failed to read queue media directory")
		}
		return
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		abs, absErr := filepath.Abs(filepath.Join(config.PathMessageQueue, entry.Name()))
		if absErr != nil {
			continue
		}
		if _, referenced := keep[abs]; referenced {
			continue
		}
		if err := os.Remove(abs); err == nil {
			removed++
		}
	}
	if removed > 0 {
		logrus.Infof("[MESSAGE_QUEUE] reclaimed %d orphaned queue media file(s)", removed)
	}
}

func (s *messageQueueScheduler) processDueDevices(ctx context.Context) {
	if s == nil || s.source == nil {
		return
	}

	for _, device := range s.source.ListMessageQueueDevices() {
		s.startIfDue(ctx, device)
	}
}

// startIfDue claims a device slot and spawns its send goroutine. Only ever
// called from the single scheduler goroutine, so the seed-then-check sequence
// below cannot interleave for one device.
func (s *messageQueueScheduler) startIfDue(ctx context.Context, device messageQueueDevice) bool {
	if s == nil || device.id == "" || device.ready == nil || device.withDevice == nil {
		return false
	}
	// A device that is not connected and logged in is not a failure; its rows
	// stay pending until it is back.
	if !device.ready() {
		return false
	}

	s.seedIfNeeded(device)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inFlight[device.id] {
		return false
	}
	if next, ok := s.nextEligible[device.id]; ok && s.now().Before(next) {
		return false
	}

	s.inFlight[device.id] = true
	go s.processOne(ctx, device)
	return true
}

// seedIfNeeded primes a device's spacing from its last successful send so the
// gap survives a restart. The repository call is made outside the mutex.
func (s *messageQueueScheduler) seedIfNeeded(device messageQueueDevice) {
	s.mu.Lock()
	if s.seeded[device.id] {
		s.mu.Unlock()
		return
	}
	s.seeded[device.id] = true
	s.mu.Unlock()

	lastSent, err := s.repo.LastSentAt(device.id)
	if err != nil {
		logrus.WithError(err).Warnf("[MESSAGE_QUEUE] failed to read last send time for device %s", device.id)
		return
	}
	if lastSent == nil {
		// Never sent through the queue: eligible immediately.
		return
	}

	next := lastSent.Add(s.randDelay(s.minDelay, s.maxDelay))
	s.mu.Lock()
	s.nextEligible[device.id] = next
	s.mu.Unlock()
}

// processOne sends at most one row for one device, so a device never emits two
// messages back to back regardless of how many are queued.
func (s *messageQueueScheduler) processOne(ctx context.Context, device messageQueueDevice) {
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, device.id)
		s.mu.Unlock()
	}()

	rows, err := s.repo.FetchPendingByDevice(device.id, s.now(), 1)
	if err != nil {
		logrus.WithError(err).Warnf("[MESSAGE_QUEUE] failed to read queue for device %s", device.id)
		return
	}
	if len(rows) == 0 {
		return
	}
	msg := rows[0]

	claimed, err := s.repo.ClaimForSending(msg.ID)
	if err != nil {
		logrus.WithError(err).Warnf("[MESSAGE_QUEUE] failed to claim queued message %d", msg.ID)
		return
	}
	if !claimed {
		// Cancelled, or taken by another process sharing this database.
		return
	}

	// Advance the spacing before dispatching, and for a failure as well as a
	// success: a rejected send may still have reached WhatsApp, so it must not
	// turn into a tight loop.
	s.mu.Lock()
	s.nextEligible[device.id] = s.now().Add(s.randDelay(s.minDelay, s.maxDelay))
	s.mu.Unlock()

	// WithoutCancel so a shutdown cannot abandon a send we already claimed;
	// sendTimeout still bounds it.
	sendCtx, cancel := context.WithTimeout(
		device.withDevice(context.WithoutCancel(ctx)), s.sendTimeout)
	defer cancel()

	messageID, sendErr := s.dispatch(sendCtx, msg)
	if sendErr != nil {
		if markErr := s.repo.MarkFailed(msg.ID, sendErr.Error()); markErr != nil {
			logrus.WithError(markErr).Errorf("[MESSAGE_QUEUE] failed to mark message %d as failed", msg.ID)
		}
		logrus.WithError(sendErr).Warnf("[MESSAGE_QUEUE] queued %s message %d for device %s to %s failed",
			msg.MessageType, msg.ID, device.id, msg.Phone)
	} else {
		if markErr := s.repo.MarkSent(msg.ID, messageID, s.now()); markErr != nil {
			logrus.WithError(markErr).Errorf("[MESSAGE_QUEUE] failed to mark message %d as sent", msg.ID)
		}
		logrus.Infof("[MESSAGE_QUEUE] sent queued %s message %d for device %s to %s",
			msg.MessageType, msg.ID, device.id, msg.Phone)
	}

	RemoveQueuedMedia(msg.MediaPath)
}

// RemoveQueuedMedia deletes the durable copy of a queued upload once its row is
// resolved. It refuses paths outside config.PathMessageQueue so a bad or tampered
// media_path can never delete anything else.
func RemoveQueuedMedia(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return
	}
	absDir, err := filepath.Abs(config.PathMessageQueue)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		logrus.Warnf("[MESSAGE_QUEUE] refusing to delete queue media outside %s: %s", config.PathMessageQueue, path)
		return
	}

	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		logrus.WithError(err).Warnf("[MESSAGE_QUEUE] failed to delete queue media %s", path)
	}
}

// randomQueueDelay picks the gap before this device's next send, inclusive of
// both bounds.
func randomQueueDelay(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int64N(int64(max-min)+1))
}
