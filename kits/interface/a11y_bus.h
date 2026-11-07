// The accessibility tree as a bus service. See a11y_bus.cpp and contracts/decisions/0017-accessibility-tree.md.
#ifndef AURORA_A11Y_BUS_H
#define AURORA_A11Y_BUS_H
#include <OS.h>
namespace aurora {
// Joins the bus (socketPath NULL: $AURORA_BUS_SOCK, else the default) and registers `service`. B_OK, or why not.
status_t a11y_start(const char* service, const char* socketPath = NULL);
void a11y_stop();
// The identity the bus stamped on this application's connection ("" when not started).
const char* a11y_identity();
}
#endif
