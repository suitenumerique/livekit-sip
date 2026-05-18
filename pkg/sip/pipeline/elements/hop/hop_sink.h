#pragma once

#include <gst/gst.h>

G_BEGIN_DECLS

#define HOP_TYPE_SINK (hop_sink_get_type())
G_DECLARE_FINAL_TYPE(HopSink, hop_sink, HOP, SINK, GstElement)

G_END_DECLS
