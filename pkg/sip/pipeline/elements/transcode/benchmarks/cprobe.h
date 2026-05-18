#ifndef CPROBE_H
#define CPROBE_H

#include <gst/gst.h>
#include <stdint.h>

typedef struct {
    GstClockTime last_entry_pts;
    int64_t     *entry_times_ns;
    int64_t     *latencies_ns;
    int          entry_count;
    int          exit_count;
    int          capacity;
} CProbe;

CProbe *cprobe_new(int capacity);
void    cprobe_free(CProbe *p);
void    cprobe_install_entry(GstPad *pad, CProbe *p);
void    cprobe_install_exit(GstPad *pad, CProbe *p);
int     cprobe_latency_count(CProbe *p);
int64_t *cprobe_latencies(CProbe *p);

#endif
