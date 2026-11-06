/* Replaces Haiku's private <syscalls.h>, which needs the whole kernel info-struct zoo (object_wait_info,
   team_usage_info, cpu_topology_node_info, ...). The Interface Kit's link layer (LinkSender.cpp) uses one
   call from it. Everything else in the real header is kernel API we do not provide. Written fresh. */
#ifndef _AURORA_SHIM_SYSCALLS_H
#define _AURORA_SHIM_SYSCALLS_H
#include <OS.h>
#ifdef __cplusplus
extern "C" {
#endif
/* Hand an area to another team. Our areas are shm-backed, so this clones it in place for the same-process
   in-process server; cross-process transfer needs area_export_fd. See kits/interface/README.md. */
extern area_id _kern_transfer_area(area_id area, void **_address, uint32 addressSpec, team_id target);
#ifdef __cplusplus
}
#endif
#endif
