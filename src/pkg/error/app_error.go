package error

import (
	"fmt"
	"net/http"
)

type LoginError string

// Error for complying the error interface
func (e LoginError) Error() string {
	return string(e)
}

// ErrCode will return the error code based on the error data type
func (e LoginError) ErrCode() string {
	return "ALREADY_LOGGED_IN"
}

// StatusCode will return the HTTP status code based on the error data type
func (e LoginError) StatusCode() int {
	return http.StatusBadRequest
}

type AuthError string

func (err AuthError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err AuthError) ErrCode() string {
	return "AUTHENTICATION_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err AuthError) StatusCode() int {
	return http.StatusUnauthorized
}

type qrChannelError string

func (err qrChannelError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err qrChannelError) ErrCode() string {
	return "QR_CHANNEL_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err qrChannelError) StatusCode() int {
	return http.StatusInternalServerError
}

type sessionSavedError string

func (err sessionSavedError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err sessionSavedError) ErrCode() string {
	return "SESSION_SAVED_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err sessionSavedError) StatusCode() int {
	return http.StatusInternalServerError
}

// deviceIDTakenError menjawab pembuatan device dengan id pilihan sendiri yang
// sudah dipakai.
//
// Lahir karena kegagalan ini SATU-SATUNYA alasan `POST /devices` menolak sebuah
// id, dan sebelumnya ia jatuh ke `500 INTERNAL_SERVER_ERROR` lewat
// `fmt.Errorf` — status yang berarti "backend rusak" untuk sesuatu yang
// sepenuhnya bisa diperbaiki pemanggil dengan mengetik id lain. Klien tidak
// punya cara membedakannya dari kerusakan sungguhan, jadi tidak ada UI yang
// bisa menawarkan field id dengan jujur.
type deviceIDTakenError string

func (err deviceIDTakenError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err deviceIDTakenError) ErrCode() string {
	return "DEVICE_ID_TAKEN"
}

// StatusCode will return the HTTP status code based on the error data type
func (err deviceIDTakenError) StatusCode() int {
	return http.StatusConflict
}

// DeviceIDTaken membangun error untuk id device yang sudah dipakai.
//
// Fungsi, bukan variabel, karena pesannya harus menyebut id-nya: "device id
// sudah dipakai" tanpa menyebut yang mana tidak menolong siapa pun yang sedang
// membuat beberapa device sekaligus.
func DeviceIDTaken(deviceID string) error {
	return deviceIDTakenError(fmt.Sprintf("device id %q is already in use", deviceID))
}

type notFoundError string

func (err notFoundError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err notFoundError) ErrCode() string {
	return "NOT_FOUND"
}

// StatusCode will return the HTTP status code based on the error data type
func (err notFoundError) StatusCode() int {
	return http.StatusNotFound
}

var (
	ErrAlreadyLoggedIn = LoginError("you are already logged in.")
	ErrNotConnected    = AuthError("you are not connect to services server, please reconnect")
	ErrNotLoggedIn     = AuthError("you are not logged in")
	ErrReconnect       = AuthError("reconnect error")
	ErrQrChannel       = qrChannelError("QR channel error")
	ErrSessionSaved    = sessionSavedError("your session have been saved, please wait to connect 2 second and refresh again")
	ErrDeviceNotFound  = notFoundError("device not found")
)
