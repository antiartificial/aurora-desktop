// Internal: what travels through a port. Not a public header.
#ifndef _MSGWIRE_H
#define _MSGWIRE_H
#include <Message.h>

enum { kPortCodeMessage = '_PMS', kPortCodeQuit = '_QIT' };
enum { kWireMagic = 'KWR1', kWireWantsReply = 1 };

struct WireHeader {
	uint32 magic; int32 token; int32 replyPort; int32 replyToken;
	int32 flags; int32 senderTeam; uint32 flatSize;
};

struct MessageAccess {
	static void setDelivery(BMessage &m, port_id rp, int32 rt, bool wants, team_id team) {
		m.fReplyPort = rp; m.fReplyToken = rt; m.fWantsReply = wants;
		m.fReplied = false; m.fDelivered = true; m.fSenderTeam = team;
	}
	static void markReplied(BMessage &m) { m.fReplied = true; }
	static port_id replyPort(const BMessage &m) { return m.fReplyPort; }
	static int32 replyToken(const BMessage &m) { return m.fReplyToken; }
};

status_t wire_send(port_id port, int32 token, const BMessage *m, port_id replyPort,
	int32 replyToken, bool wantsReply, bigtime_t timeout);
// Reads one port message. *token receives the handler token. Returns B_OK, or the
// read error; *quit is set for the internal quit code.
status_t wire_receive(port_id port, bigtime_t timeout, BMessage *out, int32 *token, bool *quit);
// "A:" + 16 hex digits of FNV-1a(signature): fits B_OS_NAME_LENGTH.
void app_port_name(const char *signature, char out[B_OS_NAME_LENGTH]);
#endif
