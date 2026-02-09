package testdata

// SDP test data extracted from debug.txt logs
// Call 1: Poly endpoint (plcm_4141838719-3915) at 09:18:14

// Call1Offer is the SDP offer received from Poly endpoint
// Timestamp: 2026-02-02T09:18:14.106+0100
// sipCallID: 4141838614-3915
const Call1Offer = `v=0
o=- 0 0 IN IP4 192.168.0.10
s=-
c=IN IP4 192.168.0.10
t=0 0
m=audio 27448 RTP/AVP 101 9 0 8
a=rtcp:27449
a=sendrecv
a=rtpmap:101 telephone-event/8000
a=rtpmap:9 G722/8000
a=rtpmap:0 PCMU/8000
a=rtpmap:8 PCMA/8000
m=video 29688 RTP/AVP 109 110 111
a=rtcp:29689
a=sendrecv
a=rtpmap:109 H264/90000
a=fmtp:109 max-fs=8192;max-mbps=490000;profile-level-id=428020;sar=13;sar-supported=13
a=rtpmap:110 H264/90000
a=fmtp:110 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=428020;sar=13;sar-supported=13
a=rtpmap:111 H264/90000
a=fmtp:111 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=640020;sar=13;sar-supported=13
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm fir
m=application 0 UDP/BFCP *
a=inactive
m=application 22164 TCP/BFCP *
a=setup:actpass
a=connection:new
a=floorctrl:c-s
a=confid:1
a=userid:2
a=floorid:1 mstrm:3
`

// Call1Answer is the SDP answer generated for Call 1
// Timestamp: 2026-02-02T09:18:14.270+0100
// Latency: 164ms from offer
const Call1Answer = `v=0
o=- 0 0 IN IP4 192.168.0.10
s=-
c=IN IP4 192.168.0.10
t=0 0
m=audio 25485 RTP/AVP 9 101
a=sendrecv
a=rtpmap:9 G722/8000
a=rtpmap:101 telephone-event/8000
m=video 27278 RTP/AVP 110
a=rtcp:27279
a=sendrecv
a=rtpmap:110 H264/90000
a=fmtp:110 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=428020;sar=13;sar-supported=13
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm fir
m=application 0 UDP/BFCP *
a=inactive
m=video 27500 RTP/AVP 110
a=rtcp:27501
a=sendonly
a=content:slides
a=label:2
a=rtpmap:110 H264/90000
a=fmtp:110 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=428020;sar=13;sar-supported=13
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm fir
m=application 20301 TCP/BFCP *
a=setup:passive
a=connection:new
a=floorctrl:s-only
a=confid:1
a=userid:1
a=floorid:1 mstrm:2
`

// Call1ReInviteOffer is the RE-INVITE offer received for Call 1
// Timestamp: 2026-02-02T09:18:14.331+0100
const Call1ReInviteOffer = `v=0
o=- 0 0 IN IP4 192.168.0.10
s=-
c=IN IP4 192.168.0.10
t=0 0
m=audio 27448 RTP/AVP 101 9 0 8
a=rtcp:27449
a=sendrecv
a=rtpmap:101 telephone-event/8000
a=rtpmap:9 G722/8000
a=rtpmap:0 PCMU/8000
a=rtpmap:8 PCMA/8000
m=video 29688 RTP/AVP 109 110 111
a=rtcp:29689
a=sendrecv
a=rtpmap:109 H264/90000
a=fmtp:109 max-fs=8192;max-mbps=490000;profile-level-id=428020;sar=13;sar-supported=13
a=rtpmap:110 H264/90000
a=fmtp:110 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=428020;sar=13;sar-supported=13
a=rtpmap:111 H264/90000
a=fmtp:111 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=640020;sar=13;sar-supported=13
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm fir
m=application 0 UDP/BFCP *
a=inactive
m=application 22164 TCP/BFCP *
a=setup:actpass
a=connection:new
a=floorctrl:c-s
a=confid:1
a=userid:2
a=floorid:1 mstrm:3
`

// Call1ReInviteAnswer is the RE-INVITE answer generated for Call 1
// Timestamp: 2026-02-02T09:18:14.332+0100
// Latency: 1ms from re-invite offer
const Call1ReInviteAnswer = `v=0
o=- 0 0 IN IP4 192.168.0.10
s=-
c=IN IP4 192.168.0.10
t=0 0
m=audio 25485 RTP/AVP 9 101
a=sendrecv
a=rtpmap:9 G722/8000
a=rtpmap:101 telephone-event/8000
m=video 27278 RTP/AVP 110
a=rtcp:27279
a=sendrecv
a=rtpmap:110 H264/90000
a=fmtp:110 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=428020;sar=13;sar-supported=13
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm fir
m=application 0 UDP/BFCP *
a=inactive
m=video 27500 RTP/AVP 110
a=rtcp:27501
a=sendonly
a=content:slides
a=label:2
a=rtpmap:110 H264/90000
a=fmtp:110 max-fs=8192;max-mbps=490000;packetization-mode=1;profile-level-id=428020;sar=13;sar-supported=13
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm fir
m=application 20301 TCP/BFCP *
a=setup:passive
a=connection:new
a=floorctrl:s-only
a=confid:1
a=userid:1
a=floorid:1 mstrm:2
`

// Call 3: livekit-sip3 endpoint at 09:22:10
// This endpoint uses UDP/BFCP and has content labels (main/slides)

// Call3Offer is the SDP offer received from livekit-sip3 endpoint
// Timestamp: 2026-02-02T09:22:10.422+0100
// sipCallID: 5ae19f50c2fc4ecf336f129612b15226
const Call3Offer = `v=0
o=- 0 0 IN IP4 192.168.0.10
s=-
c=IN IP4 192.168.0.10
t=0 0
m=audio 27080 RTP/AVP 0 101 9 8
a=rtcp:27081
a=sendrecv
a=rtpmap:0 PCMU/8000
a=rtpmap:101 telephone-event/8000
a=rtpmap:9 G722/8000
a=rtpmap:8 PCMA/8000
m=video 21520 RTP/AVP 97 126
a=rtcp:21521
a=sendrecv
a=content:main
a=label:11
a=rtpmap:97 H264/90000
a=fmtp:97 max-br=6000;max-dpb=16320;max-fps=6000;max-fs=8160;max-mbps=490000;max-smbps=490000;packetization-mode=0;profile-level-id=428016
a=rtpmap:126 H264/90000
a=fmtp:126 max-br=6000;max-dpb=16320;max-fps=6000;max-fs=8160;max-mbps=490000;max-smbps=490000;packetization-mode=1;profile-level-id=428016
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm pan
m=application 0 UDP/BFCP *
a=inactive
m=video 28734 RTP/AVP 97 126
a=rtcp:28735
a=sendrecv
a=content:slides
a=label:12
a=rtpmap:97 H264/90000
a=fmtp:97 max-br=6000;max-dpb=64800;max-fps=6000;max-fs=32400;max-mbps=259200;max-smbps=259200;packetization-mode=0;profile-level-id=428016
a=rtpmap:126 H264/90000
a=fmtp:126 max-br=6000;max-dpb=64800;max-fps=6000;max-fs=32400;max-mbps=259200;max-smbps=259200;packetization-mode=1;profile-level-id=428016
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
m=application 23908 UDP/BFCP *
a=setup:actpass
a=connection:new
a=floorctrl:c-s
a=confid:1
a=userid:2
a=floorid:2 mstrm:12
`

// Call3Answer is the SDP answer generated for Call 3
// Timestamp: 2026-02-02T09:22:10.512+0100
// Latency: 90ms from offer
const Call3Answer = `v=0
o=- 0 0 IN IP4 192.168.0.10
s=-
c=IN IP4 192.168.0.10
t=0 0
m=audio 25000 RTP/AVP 9 101
a=sendrecv
a=rtpmap:9 G722/8000
a=rtpmap:101 telephone-event/8000
m=video 27000 RTP/AVP 126
a=rtcp:27001
a=sendrecv
a=content:main
a=label:11
a=rtpmap:126 H264/90000
a=fmtp:126 max-br=6000;max-dpb=16320;max-fps=6000;max-fs=8160;max-mbps=490000;max-smbps=490000;packetization-mode=1;profile-level-id=428016
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
a=rtcp-fb:* ccm pan
m=application 0 UDP/BFCP *
a=inactive
m=video 28000 RTP/AVP 126
a=rtcp:28001
a=sendrecv
a=content:slides
a=label:12
a=rtpmap:126 H264/90000
a=fmtp:126 max-br=6000;max-dpb=64800;max-fps=6000;max-fs=32400;max-mbps=259200;max-smbps=259200;packetization-mode=1;profile-level-id=428016
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
m=application 20000 UDP/BFCP *
a=setup:passive
a=connection:new
a=floorctrl:s-only
a=confid:1
a=userid:1
a=floorid:2 mstrm:12
`
