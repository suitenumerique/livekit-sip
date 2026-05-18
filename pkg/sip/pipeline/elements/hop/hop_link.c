#include "hop_link.h"
#include "hop_src.h"
#include "hop_sink.h"

struct _HopLink
{
    GObject parent;
    GRWLock lock;
    GstPad *sink_pad; /* hopsink's sink pad */
    GstPad *src_pad;  /* hopsrc's src pad */
};

G_DEFINE_TYPE(HopLink, hop_link, G_TYPE_OBJECT)

static void
hop_link_finalize(GObject *obj)
{
    HopLink *l = HOP_LINK(obj);
    g_rw_lock_clear(&l->lock);
    G_OBJECT_CLASS(hop_link_parent_class)->finalize(obj);
}

static void
hop_link_class_init(HopLinkClass *klass)
{
    G_OBJECT_CLASS(klass)->finalize = hop_link_finalize;
}

static void
hop_link_init(HopLink *l)
{
    g_rw_lock_init(&l->lock);
    l->sink_pad = NULL;
    l->src_pad = NULL;
}

HopLink *
hop_link_new(void)
{
    return g_object_new(HOP_TYPE_LINK, NULL);
}

void
hop_link_set_pad(HopLink *l, GstPadDirection dir, GstPad *pad)
{
    g_rw_lock_writer_lock(&l->lock);
    if (dir == GST_PAD_SINK)
        l->sink_pad = pad;
    else if (dir == GST_PAD_SRC)
        l->src_pad = pad;
    g_rw_lock_writer_unlock(&l->lock);
}

GstPad *
hop_link_acquire_partner(HopLink *l, GstPadDirection my_dir)
{
    GstPad *partner = NULL;
    g_rw_lock_reader_lock(&l->lock);
    if (my_dir == GST_PAD_SINK)
        partner = l->src_pad;
    else if (my_dir == GST_PAD_SRC)
        partner = l->sink_pad;
    if (G_LIKELY(partner != NULL))
        gst_object_ref(partner);
    g_rw_lock_reader_unlock(&l->lock);
    return partner;
}

/* Element-side helpers — defined in hop_src.c / hop_sink.c — that swap
 * the element's current link for a new shared one and re-register the
 * element's pad in the new link. */
extern void _hop_src_replace_link(GstElement *src, HopLink *l);
extern void _hop_sink_replace_link(GstElement *sink, HopLink *l);

gboolean
hop_pair_bind(GstElement *sink, GstElement *src)
{
    if (G_UNLIKELY(!HOP_IS_SINK(sink) || !HOP_IS_SRC(src)))
        return FALSE;

    HopLink *shared = hop_link_new();
    _hop_sink_replace_link(sink, shared);
    _hop_src_replace_link(src, shared);
    /* After replace_link, both elements own a ref. Drop ours. */
    g_object_unref(shared);
    return TRUE;
}
