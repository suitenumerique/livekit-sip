#include <gst/gst.h>

static GstPadProbeReturn
fix_pts_probe(GstPad *pad, GstPadProbeInfo *info, gpointer user_data)
{
    GstClockTime *last_pts = (GstClockTime *)user_data;
    GstBuffer *buf = GST_PAD_PROBE_INFO_BUFFER(info);

    buf = gst_buffer_make_writable(buf);
    GST_PAD_PROBE_INFO_DATA(info) = buf;

    if (GST_BUFFER_PTS_IS_VALID(buf)) {
        *last_pts = GST_BUFFER_PTS(buf);
    } else if (GST_CLOCK_TIME_IS_VALID(*last_pts)) {
        GST_BUFFER_PTS(buf) = *last_pts;
    }

    return GST_PAD_PROBE_OK;
}

static void
fix_pts_probe_destroy(gpointer data)
{
    g_free(data);
}

void
vp9_add_fix_pts_probe(GstElement *element)
{
    GstPad *srcpad = gst_element_get_static_pad(element, "src");
    GstClockTime *state = g_new(GstClockTime, 1);
    *state = GST_CLOCK_TIME_NONE;

    gst_pad_add_probe(srcpad, GST_PAD_PROBE_TYPE_BUFFER,
        fix_pts_probe, state, fix_pts_probe_destroy);
    gst_object_unref(srcpad);
}
