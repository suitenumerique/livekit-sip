#pragma once

#include <gst/gst.h>

G_BEGIN_DECLS

#define HOP_TYPE_SRC (hop_src_get_type())
G_DECLARE_FINAL_TYPE(HopSrc, hop_src, HOP, SRC, GstElement)

G_END_DECLS
