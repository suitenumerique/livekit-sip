/*
 * HopSrc — plain GstElement with one src pad. No streaming task, no
 * thread, no queue. The paired HopSink forwards downstream buffers and
 * events to this element's pad synchronously via gst_pad_push() /
 * gst_pad_push_event(). This element only handles the upstream
 * direction: its src pad's event_func and query_func forward upstream
 * events and queries across the hop to the paired HopSink's sink pad.
 */

#include "hop_src.h"
#include "hop_link.h"

struct _HopSrc
{
    GstElement parent;
    GstPad *srcpad;
    HopLink *link;
};

G_DEFINE_TYPE(HopSrc, hop_src, GST_TYPE_ELEMENT)

static GstStaticPadTemplate src_template =
    GST_STATIC_PAD_TEMPLATE("src",
                            GST_PAD_SRC,
                            GST_PAD_ALWAYS,
                            GST_STATIC_CAPS_ANY);

static gboolean
hop_src_event(GstPad *pad, GstObject *parent, GstEvent *ev)
{
    HopSrc *s = HOP_SRC(parent);
    GstPad *partner = hop_link_acquire_partner(s->link, GST_PAD_SRC);
    if (G_UNLIKELY(!partner))
    {
        /* Partner gone — fall back to default to keep pad sane. */
        gst_event_unref(ev);
        return TRUE;
    }
    gboolean ok = gst_pad_push_event(partner, ev);
    gst_object_unref(partner);
    (void)pad;
    return ok;
}

static gboolean
hop_src_query(GstPad *pad, GstObject *parent, GstQuery *q)
{
    HopSrc *s = HOP_SRC(parent);
    GstPad *partner = hop_link_acquire_partner(s->link, GST_PAD_SRC);
    if (G_UNLIKELY(!partner))
        return gst_pad_query_default(pad, parent, q);
    gboolean ok = gst_pad_peer_query(partner, q);
    gst_object_unref(partner);
    return ok;
}

static void
hop_src_dispose(GObject *obj)
{
    HopSrc *s = HOP_SRC(obj);
    if (s->link)
    {
        hop_link_set_pad(s->link, GST_PAD_SRC, NULL);
        g_object_unref(s->link);
        s->link = NULL;
    }
    G_OBJECT_CLASS(hop_src_parent_class)->dispose(obj);
}

static void
hop_src_class_init(HopSrcClass *klass)
{
    GObjectClass *gc = G_OBJECT_CLASS(klass);
    GstElementClass *ec = GST_ELEMENT_CLASS(klass);

    gc->dispose = hop_src_dispose;

    gst_element_class_add_static_pad_template(ec, &src_template);
    gst_element_class_set_static_metadata(ec,
                                          "Hop Src", "Source/Generic",
                                          "Passthrough src half — externally driven by paired HopSink",
                                          "livekit-sip");
}

static void
hop_src_init(HopSrc *s)
{
    s->srcpad = gst_pad_new_from_static_template(&src_template, "src");
    gst_pad_set_event_function(s->srcpad, hop_src_event);
    gst_pad_set_query_function(s->srcpad, hop_src_query);
    gst_element_add_pad(GST_ELEMENT(s), s->srcpad);

    s->link = hop_link_new();
    hop_link_set_pad(s->link, GST_PAD_SRC, s->srcpad);
}

void
_hop_src_replace_link(GstElement *elem, HopLink *new_link)
{
    HopSrc *s = HOP_SRC(elem);
    /* Clear the old link's pointer, drop our ref, adopt the new one. */
    hop_link_set_pad(s->link, GST_PAD_SRC, NULL);
    g_object_unref(s->link);
    s->link = g_object_ref(new_link);
    hop_link_set_pad(s->link, GST_PAD_SRC, s->srcpad);
}
