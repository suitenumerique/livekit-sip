/*
 * HopSink — plain GstElement with one sink pad. Forwards downstream
 * buffers, events, and queries synchronously across the hop to the
 * paired HopSrc's src pad. No streaming task, no thread, no queue.
 */

#include "hop_sink.h"
#include "hop_link.h"

struct _HopSink
{
    GstElement parent;
    GstPad *sinkpad;
    HopLink *link;
};

G_DEFINE_TYPE(HopSink, hop_sink, GST_TYPE_ELEMENT)

static GstStaticPadTemplate sink_template =
    GST_STATIC_PAD_TEMPLATE("sink",
                            GST_PAD_SINK,
                            GST_PAD_ALWAYS,
                            GST_STATIC_CAPS_ANY);

static GstFlowReturn
hop_sink_chain(GstPad *pad, GstObject *parent, GstBuffer *buf)
{
    HopSink *s = HOP_SINK(parent);
    GstPad *partner = hop_link_acquire_partner(s->link, GST_PAD_SINK);
    if (G_UNLIKELY(!partner))
    {
        gst_buffer_unref(buf);
        return GST_FLOW_FLUSHING;
    }
    GstFlowReturn ret = gst_pad_push(partner, buf);
    gst_object_unref(partner);
    (void)pad;
    return ret;
}

static gboolean
hop_sink_event(GstPad *pad, GstObject *parent, GstEvent *ev)
{
    HopSink *s = HOP_SINK(parent);
    GstPad *partner = hop_link_acquire_partner(s->link, GST_PAD_SINK);
    if (G_UNLIKELY(!partner))
    {
        /* Drop the event but report success so upstream doesn't error. */
        gst_event_unref(ev);
        return TRUE;
    }
    gboolean ok = gst_pad_push_event(partner, ev);
    gst_object_unref(partner);
    (void)pad;
    return ok;
}

static gboolean
hop_sink_query(GstPad *pad, GstObject *parent, GstQuery *q)
{
    HopSink *s = HOP_SINK(parent);
    GstPad *partner = hop_link_acquire_partner(s->link, GST_PAD_SINK);
    if (G_UNLIKELY(!partner))
        return gst_pad_query_default(pad, parent, q);
    gboolean ok = gst_pad_peer_query(partner, q);
    gst_object_unref(partner);
    return ok;
}

static void
hop_sink_dispose(GObject *obj)
{
    HopSink *s = HOP_SINK(obj);
    if (s->link)
    {
        hop_link_set_pad(s->link, GST_PAD_SINK, NULL);
        g_object_unref(s->link);
        s->link = NULL;
    }
    G_OBJECT_CLASS(hop_sink_parent_class)->dispose(obj);
}

static void
hop_sink_class_init(HopSinkClass *klass)
{
    GObjectClass *gc = G_OBJECT_CLASS(klass);
    GstElementClass *ec = GST_ELEMENT_CLASS(klass);

    gc->dispose = hop_sink_dispose;

    gst_element_class_add_static_pad_template(ec, &sink_template);
    gst_element_class_set_static_metadata(ec,
                                          "Hop Sink", "Sink/Generic",
                                          "Passthrough sink half — forwards synchronously to paired HopSrc",
                                          "livekit-sip");
}

static void
hop_sink_init(HopSink *s)
{
    s->sinkpad = gst_pad_new_from_static_template(&sink_template, "sink");
    gst_pad_set_chain_function(s->sinkpad, hop_sink_chain);
    gst_pad_set_event_function(s->sinkpad, hop_sink_event);
    gst_pad_set_query_function(s->sinkpad, hop_sink_query);
    gst_element_add_pad(GST_ELEMENT(s), s->sinkpad);

    s->link = hop_link_new();
    hop_link_set_pad(s->link, GST_PAD_SINK, s->sinkpad);
}

void
_hop_sink_replace_link(GstElement *elem, HopLink *new_link)
{
    HopSink *s = HOP_SINK(elem);
    hop_link_set_pad(s->link, GST_PAD_SINK, NULL);
    g_object_unref(s->link);
    s->link = g_object_ref(new_link);
    hop_link_set_pad(s->link, GST_PAD_SINK, s->sinkpad);
}
