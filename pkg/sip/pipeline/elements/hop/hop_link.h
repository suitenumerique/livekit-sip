#pragma once

#include <gst/gst.h>

G_BEGIN_DECLS

/* Shared, ref-counted connective tissue between a HopSink and a HopSrc.
 *
 * HopLink is a plain GObject — refcounting goes through g_object_ref /
 * g_object_unref. Each element holds one ref for its entire lifetime.
 *
 * The link stores raw pointers to the two pads (no refcount on the pads
 * themselves) — each element clears its own slot under the writer lock
 * during dispose, while the pad is still alive.
 *
 * hop_link_acquire_partner() takes the reader lock briefly, reads the
 * partner pad pointer, and gst_object_ref()s it before releasing the
 * lock, so the partner pad survives the forward operation even if the
 * partner element disposes concurrently. Concurrent readers don't
 * contend; writers (set_pad) only run on init/dispose/bind, so the
 * hot path is fully parallel.
 *
 * No lock is ever held across a gst_pad_push or peer_query call. */

#define HOP_TYPE_LINK (hop_link_get_type())
G_DECLARE_FINAL_TYPE(HopLink, hop_link, HOP, LINK, GObject)

HopLink *hop_link_new(void);

/* Set or clear (pad == NULL) the slot for the given direction. */
void     hop_link_set_pad(HopLink *l, GstPadDirection dir, GstPad *pad);

/* Returns a ref'd partner pad, or NULL if the partner is gone.
 * my_dir is the direction of the *caller's* pad — partner is the other one. */
GstPad  *hop_link_acquire_partner(HopLink *l, GstPadDirection my_dir);

/* Bind two freshly-created hop elements into a pair. Releases each
 * element's per-init throwaway link and replaces it with a fresh shared
 * one. Returns FALSE if either element is the wrong type. */
gboolean hop_pair_bind(GstElement *sink, GstElement *src);

G_END_DECLS
