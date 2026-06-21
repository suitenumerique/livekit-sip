package apperror

import (
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var appErrorQuark = glib.QuarkFromString("APP_ERROR")

type appErrorDomain struct{}

func (appErrorDomain) ToDomainQuark() glib.Quark {
	return appErrorQuark
}

var AppErrorDomain gst.Domain = appErrorDomain{}

const (
	AppError gst.ErrorCode = iota + 1
	AppFatalError
)
