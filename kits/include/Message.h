/*
 * BMessage: typed, named, multi-valued fields; flat form for ports.
 * Public API shape follows Haiku's MIT-licensed Message.h (a subset).
 * Implementation is original; the flat format is NOT Haiku's.
 */
#ifndef _MESSAGE_H
#define _MESSAGE_H
#include <OS.h>
#include <TypeConstants.h>
#include <AppDefs.h>
#include <string>
#include <unistd.h>
#include <vector>

class BHandler;
class BMessenger;

class BMessage {
public:
	BMessage();
	BMessage(uint32 what);
	BMessage(const BMessage &other);
	BMessage &operator=(const BMessage &other);
	~BMessage();

	uint32 what;

	bool Equals(const BMessage &other) const; // what + all fields, in order
	bool IsEmpty() const;
	int32 CountNames(type_code type) const;
	status_t GetInfo(const char *name, type_code *type, int32 *count = NULL) const;
	/* By index, in insertion order (Haiku's signature). *nameFound points into the message. */
	status_t GetInfo(type_code typeRequested, int32 index, char **nameFound, type_code *typeFound,
		int32 *countFound = NULL) const;
	status_t RemoveName(const char *name);
	status_t MakeEmpty();
	void PrintToStream() const;

	status_t AddInt8(const char *name, int8 v);
	status_t AddInt16(const char *name, int16 v);
	status_t AddInt32(const char *name, int32 v);
	status_t AddInt64(const char *name, int64 v);
	status_t AddBool(const char *name, bool v);
	status_t AddFloat(const char *name, float v);
	status_t AddDouble(const char *name, double v);
	status_t AddString(const char *name, const char *v);
	status_t AddData(const char *name, type_code type, const void *data, ssize_t size);
	status_t AddMessage(const char *name, const BMessage *message);

	status_t FindInt8(const char *name, int8 *v) const { return FindInt8(name, 0, v); }
	status_t FindInt8(const char *name, int32 index, int8 *v) const;
	status_t FindInt16(const char *name, int16 *v) const { return FindInt16(name, 0, v); }
	status_t FindInt16(const char *name, int32 index, int16 *v) const;
	status_t FindInt32(const char *name, int32 *v) const { return FindInt32(name, 0, v); }
	status_t FindInt32(const char *name, int32 index, int32 *v) const;
	status_t FindInt64(const char *name, int64 *v) const { return FindInt64(name, 0, v); }
	status_t FindInt64(const char *name, int32 index, int64 *v) const;
	status_t FindBool(const char *name, bool *v) const { return FindBool(name, 0, v); }
	status_t FindBool(const char *name, int32 index, bool *v) const;
	status_t FindFloat(const char *name, float *v) const { return FindFloat(name, 0, v); }
	status_t FindFloat(const char *name, int32 index, float *v) const;
	status_t FindDouble(const char *name, double *v) const { return FindDouble(name, 0, v); }
	status_t FindDouble(const char *name, int32 index, double *v) const;
	/* The returned pointers stay valid until the message is modified. */
	status_t FindString(const char *name, const char **v) const { return FindString(name, 0, v); }
	status_t FindString(const char *name, int32 index, const char **v) const;
	status_t FindData(const char *name, type_code type, const void **data, ssize_t *size) const
		{ return FindData(name, type, 0, data, size); }
	status_t FindData(const char *name, type_code type, int32 index, const void **data,
		ssize_t *size) const;
	status_t FindMessage(const char *name, BMessage *message) const { return FindMessage(name, 0, message); }
	status_t FindMessage(const char *name, int32 index, BMessage *message) const;

	ssize_t FlattenedSize() const;
	status_t Flatten(char *buffer, ssize_t size) const;
	status_t Unflatten(const char *buffer, ssize_t size);

	/* Reply support: valid for a message received from a port. */
	bool IsSourceWaiting() const { return fReplyPort >= 0 && fWantsReply && !fReplied; }
	bool IsSourceRemote() const { return fSenderTeam != getpid(); }
	bool WasDelivered() const { return fDelivered; }
	status_t SendReply(BMessage *reply, BHandler *replyTo = NULL, bigtime_t timeout = B_INFINITE_TIMEOUT);
	status_t SendReply(uint32 command, BHandler *replyTo = NULL);
	BMessenger ReturnAddress() const;

private:
	struct Field {
		std::string name; type_code type;
		std::vector<std::string> items;   // raw bytes per item
	};
	friend class BLooper; friend class BMessenger;
	const Field *find(const char *name) const;
	Field *findOrAdd(const char *name, type_code type, status_t *err);
	status_t addRaw(const char *name, type_code type, const void *p, size_t n);
	status_t findRaw(const char *name, type_code type, int32 index, const std::string **out) const;
	template <class T> status_t addPod(const char *name, type_code type, T v)
		{ return addRaw(name, type, &v, sizeof v); }
	template <class T> status_t findPod(const char *name, type_code type, int32 index, T *v) const;

	std::vector<Field> fFields;
	// delivery metadata, set by the looper when a message arrives
	port_id fReplyPort = -1; int32 fReplyToken = -1; bool fWantsReply = false;
	bool fReplied = false, fDelivered = false; team_id fSenderTeam = 0;
	friend struct MessageAccess;
};
#endif
