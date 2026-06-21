package sipbin

import (
	"fmt"
	"net"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/samber/lo"
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"port-start",
		"Port start",
		"The start of the port range",
		0,
		0xFFFF,
		1024,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewUintParam(
		"port-end",
		"Port end",
		"The end of the port range",
		0,
		0xFFFF,
		65535,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"ip",
		"IP",
		"The IP address to advertize in the SDP answer",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"bind-ip",
		"Bind IP",
		"The IP address to bind to for media ports (defaults to ip property)",
		nil,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewBoxedParam(
		"formats",
		"Formats",
		"An array of supported media formats",
		glib.TYPE_ARRAY,
		glib.ParameterReadable|glib.ParameterWritable,
	),
	glib.NewStringParam(
		"session-id",
		"Session ID",
		"The session ID to use in the SDP answer",
		nil,
		glib.ParameterReadable,
	),
	glib.NewIntParam(
		"transaction-pending",
		"Transaction Pending",
		"Whether a SIP transaction is currently pending",
		int(TransactionPendingKindNone),
		int(TransactionPendingKindAnswer),
		int(TransactionPendingKindNone),
		glib.ParameterReadable,
	),
}

type config struct {
	portStart uint16
	portEnd   uint16
	ip        net.IP
	bindIP    net.IP
	formats   []*gst.Caps
	sessionID string
}

func (e *SipBin) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	e.mu.Lock()
	defer e.mu.Unlock()
	param := properties[id]
	switch param.Name() {
	case "port-start":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting port-start property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for port-start property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for port-start property\nvalue=%d", val))
			return
		}
		e.portStart = uint16(val)
	case "port-end":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting port-end property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for port-end property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for port-end property\nvalue=%d", val))
			return
		}
		e.portEnd = uint16(val)
	case "ip":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting ip property value\nerr=%v", err))
			return
		}
		val, ok := gv.(string)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for ip property")
			return
		}
		ip := net.ParseIP(val)
		if ip == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid IP address for ip property\nvalue=%s", val))
			return
		}
		e.ip = ip
	case "bind-ip":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting bind-ip property value\nerr=%v", err))
			return
		}
		val, ok := gv.(string)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for bind-ip property")
			return
		}
		ip := net.ParseIP(val)
		if ip == nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid IP address for bind-ip property\nvalue=%s", val))
			return
		}
		e.bindIP = ip
	case "formats":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting formats property value\nerr=%v", err))
			return
		}
		val, ok := gv.(*glib.Array)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for formats property")
			return
		}
		values, err := val.Values()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting values from formats property array\nerr=%v", err))
			return
		}
		formats := lo.Filter(lo.Map(values, func(v interface{}, _ int) *gst.Caps {
			caps, ok := v.(*gst.Caps)
			if !ok {
				self.Log(CAT, gst.LevelWarning, "Invalid format in formats property array")
				return nil
			}
			return caps.Copy()
		}), func(c *gst.Caps, _ int) bool { return c != nil })
		if len(formats) == 0 {
			self.Log(CAT, gst.LevelWarning, "No valid formats provided in formats property array")
			return
		}
		e.formats = formats
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nname=%s", param.Name()))
	}
}

func (e *SipBin) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
	e.mu.Lock()
	defer e.mu.Unlock()
	param := properties[id]
	switch param.Name() {
	case "port-start":
		value, err := glib.GValue(uint(e.portStart))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting port-start property value\nerr=%v", err))
			return nil
		}
		return value
	case "port-end":
		value, err := glib.GValue(uint(e.portEnd))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting port-end property value\nerr=%v", err))
			return nil
		}
		return value
	case "ip":
		if e.ip == nil {
			return nil
		}
		value, err := glib.GValue(e.ip.String())
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting ip property value\nerr=%v", err))
			return nil
		}
		return value
	case "bind-ip":
		if e.bindIP == nil {
			return nil
		}
		value, err := glib.GValue(e.bindIP.String())
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting bind-ip property value\nerr=%v", err))
			return nil
		}
		return value
	case "formats":
		arr, err := glib.NewArray(lo.Map(e.formats, func(c *gst.Caps, _ int) interface{} { return c }))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error creating array for formats property\nerr=%v", err))
			return nil
		}
		value, err := glib.GValue(arr)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting formats property value\nerr=%v", err))
			return nil
		}
		return value
	case "session-id":
		value, err := glib.GValue(e.sessionID)
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting session-id property value\nerr=%v", err))
			return nil
		}
		return value
	case "transaction-pending":
		pending, unlock := e.transaction.GetPending()
		defer unlock()
		value, err := glib.GValue(int(pending))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting transaction-pending property value\nerr=%v", err))
			return nil
		}
		return value
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nname=%s", param.Name()))
		return nil
	}
}
