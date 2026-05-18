// Pure-C pad probe for measuring EUT latency in the child process.
//
// Two probes are installed on the EUT bin's ghost pads:
//   entry: EUT sink pad (where buffers enter the element-under-test)
//   exit:  EUT src pad  (where buffers leave)
//
// Entry timestamps are recorded with clock_gettime(CLOCK_MONOTONIC).
// Multiple RTP packets per frame share a PTS — the entry probe
// deduplicates so exactly one timestamp is recorded per frame. The
// exit probe FIFO-pairs with entry timestamps to compute per-frame
// latency. This is correct because no element in the chain reorders
// frames.
//
// Thread safety: entry and exit probes fire on different GStreamer
// streaming threads. entry_count is published with release semantics
// and read with acquire semantics so the exit probe always sees a
// consistent entry_times_ns[exit_count] value.

#include "cprobe.h"
#include <time.h>

static int64_t
mono_ns(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static void
cprobe_grow(CProbe *p)
{
    int newcap = p->capacity ? p->capacity * 2 : 256;
    p->entry_times_ns = g_realloc(p->entry_times_ns, newcap * sizeof(int64_t));
    p->latencies_ns   = g_realloc(p->latencies_ns,   newcap * sizeof(int64_t));
    p->capacity = newcap;
}

CProbe *
cprobe_new(int capacity)
{
    CProbe *p = g_new0(CProbe, 1);
    p->last_entry_pts = GST_CLOCK_TIME_NONE;
    if (capacity > 0) {
        p->capacity = capacity;
        p->entry_times_ns = g_new(int64_t, capacity);
        p->latencies_ns   = g_new(int64_t, capacity);
    }
    return p;
}

void
cprobe_free(CProbe *p)
{
    if (!p) return;
    g_free(p->entry_times_ns);
    g_free(p->latencies_ns);
    g_free(p);
}

static void
cprobe_record_entry(CProbe *p, GstBuffer *buf)
{
    if (!buf) return;
    GstClockTime pts = GST_BUFFER_PTS(buf);
    if (!GST_CLOCK_TIME_IS_VALID(pts)) return;
    if (pts == p->last_entry_pts) return;

    p->last_entry_pts = pts;
    if (p->entry_count >= p->capacity)
        cprobe_grow(p);

    p->entry_times_ns[p->entry_count] = mono_ns();
    __atomic_store_n(&p->entry_count, p->entry_count + 1, __ATOMIC_RELEASE);
}

static void
cprobe_record_exit(CProbe *p, GstBuffer *buf)
{
    if (!buf) return;
    int ec = __atomic_load_n(&p->entry_count, __ATOMIC_ACQUIRE);
    if (p->exit_count >= ec) return;

    int64_t now = mono_ns();
    p->latencies_ns[p->exit_count] = now - p->entry_times_ns[p->exit_count];
    p->exit_count++;
}

static GstPadProbeReturn
entry_probe_cb(GstPad *pad, GstPadProbeInfo *info, gpointer user_data)
{
    CProbe *p = (CProbe *)user_data;
    (void)pad;

    if (info->type & GST_PAD_PROBE_TYPE_BUFFER_LIST) {
        GstBufferList *list = GST_PAD_PROBE_INFO_BUFFER_LIST(info);
        guint n = gst_buffer_list_length(list);
        for (guint i = 0; i < n; i++)
            cprobe_record_entry(p, gst_buffer_list_get(list, i));
    } else {
        cprobe_record_entry(p, GST_PAD_PROBE_INFO_BUFFER(info));
    }
    return GST_PAD_PROBE_OK;
}

static GstPadProbeReturn
exit_probe_cb(GstPad *pad, GstPadProbeInfo *info, gpointer user_data)
{
    CProbe *p = (CProbe *)user_data;
    (void)pad;

    if (info->type & GST_PAD_PROBE_TYPE_BUFFER_LIST) {
        GstBufferList *list = GST_PAD_PROBE_INFO_BUFFER_LIST(info);
        guint n = gst_buffer_list_length(list);
        for (guint i = 0; i < n; i++)
            cprobe_record_exit(p, gst_buffer_list_get(list, i));
    } else {
        cprobe_record_exit(p, GST_PAD_PROBE_INFO_BUFFER(info));
    }
    return GST_PAD_PROBE_OK;
}

void
cprobe_install_entry(GstPad *pad, CProbe *p)
{
    gst_pad_add_probe(pad,
        GST_PAD_PROBE_TYPE_BUFFER | GST_PAD_PROBE_TYPE_BUFFER_LIST,
        entry_probe_cb, p, NULL);
}

void
cprobe_install_exit(GstPad *pad, CProbe *p)
{
    gst_pad_add_probe(pad,
        GST_PAD_PROBE_TYPE_BUFFER | GST_PAD_PROBE_TYPE_BUFFER_LIST,
        exit_probe_cb, p, NULL);
}

int
cprobe_latency_count(CProbe *p)
{
    return p ? p->exit_count : 0;
}

int64_t *
cprobe_latencies(CProbe *p)
{
    return p ? p->latencies_ns : NULL;
}
