package bfcpserver

import (
	"fmt"
	"math"
	"net"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
)

var properties = []*glib.ParamSpec{
	glib.NewUintParam(
		"port-start",
		"Port start",
		"The start of the port range",
		0,
		0xFFFF,
		1024,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstruct,
	),
	glib.NewUintParam(
		"port-end",
		"Port end",
		"The end of the port range",
		2,
		0xFFFF,
		65535,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstruct,
	),
	glib.NewUintParam(
		"port",
		"Port",
		"The port of the BFCP server",
		0,
		0xFFFF,
		0,
		glib.ParameterReadable,
	),
	glib.NewStringParam(
		"bind-ip",
		"Bind IP",
		"The IP address to bind to",
		nil,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstruct,
	),
	glib.NewUintParam(
		"floor-id",
		"Floor ID",
		"The BFCP floor ID to use for the server",
		0,
		math.MaxUint16,
		1,
		glib.ParameterReadable|glib.ParameterWritable|glib.ParameterConstruct,
	),
}

type props struct {
	portStart uint16
	portEnd   uint16
	port      uint16
	bindIP    net.IP
	floorID   uint16
}

func (e *BFCPServer) SetProperty(instance *glib.Object, id uint, value *glib.Value) {
	self := gst.ToGstBin(instance)
	param := properties[id]

	if e.constructed {
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Attempt to set property after BFCP server has been created, ignoring\nproperty=%s", param.Name()))
		return
	}
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
	case "port":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting port property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for port property")
			return
		}
		if val > 0xFFFF {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for port property\nvalue=%d", val))
			return
		}
		e.port = uint16(val)
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
	case "floor-id":
		gv, err := value.GoValue()
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting floor-id property value\nerr=%v", err))
			return
		}
		val, ok := gv.(uint)
		if !ok {
			self.Log(CAT, gst.LevelError, "Invalid type for floor-id property")
			return
		}
		if val > math.MaxUint16 {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Invalid value for floor-id property\nvalue=%d", val))
			return
		}
		e.floorID = uint16(val)
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nproperty=%s", param.Name()))
	}
}

func (e *BFCPServer) GetProperty(instance *glib.Object, id uint) *glib.Value {
	self := gst.ToGstBin(instance)
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
	case "port":
		value, err := glib.GValue(uint(e.port))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting port property value\nerr=%v", err))
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
	case "floor-id":
		value, err := glib.GValue(uint(e.floorID))
		if err != nil {
			self.Log(CAT, gst.LevelError, fmt.Sprintf("Error getting floor-id property value\nerr=%v", err))
			return nil
		}
		return value
	default:
		self.Log(CAT, gst.LevelWarning, fmt.Sprintf("Unknown property\nproperty=%s", param.Name()))
		return nil
	}
}
