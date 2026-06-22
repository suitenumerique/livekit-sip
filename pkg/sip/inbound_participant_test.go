package sip

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/sipgo/sip"
)

func TestSdpOriginUsername(t *testing.T) {
	require.Equal(t, "X30", sdpOriginUsername([]byte("v=0\r\no=X30 1660198437 0 IN IP4 10.12.0.49\r\ns=-\r\n")))
	require.Equal(t, "CiscoSystemsCCM-SIP", sdpOriginUsername([]byte("o=CiscoSystemsCCM-SIP 277 1 IN IP4 172.16.100.11\r\n")))
	require.Equal(t, "", sdpOriginUsername([]byte("v=0\r\no=- 1 1 IN IP4 1.2.3.4\r\n")))
	require.Equal(t, "", sdpOriginUsername([]byte("v=0\r\ns=-\r\n")))
	require.Equal(t, "", sdpOriginUsername(nil))
}

func TestSipIdentitySlug(t *testing.T) {
	require.Equal(t, "82-65-20-145", sipIdentitySlug("82.65.20.145"))
	require.Equal(t, "x30", sipIdentitySlug("x30"))
	require.Equal(t, "a-b", sipIdentitySlug("a..b")) // run collapses to one dash
	require.Equal(t, "", sipIdentitySlug("..."))
	require.Equal(t, "", sipIdentitySlug(""))
}

func TestSipFallbackParticipant_ModelAndHosts(t *testing.T) {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Host: "10.12.0.49"})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Host: "192.168.0.104"}})
	req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "82.65.20.145", Port: 37524}})
	req.SetBody([]byte("v=0\r\no=X30 1660198437 0 IN IP4 10.12.0.49\r\nm=audio 5000 RTP/AVP 0\r\n"))

	id, name := sipFallbackParticipant(req)
	require.Equal(t, "sip_x30_82-65-20-145_192-168-0-104", id)
	require.Equal(t, "X30 192.168.0.104", name)
}

func TestSipFallbackParticipant_NoModelNoContact(t *testing.T) {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Host: "10.12.0.49"})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Host: "192.168.0.104"}})
	req.SetBody([]byte("v=0\r\no=- 1 1 IN IP4 10.12.0.49\r\n"))

	id, name := sipFallbackParticipant(req)
	require.Equal(t, "sip_192-168-0-104", id)
	require.Equal(t, "Phone 192.168.0.104", name)
}
