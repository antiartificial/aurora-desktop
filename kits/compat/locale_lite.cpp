// The part of Haiku's Locale Kit that BColumnListView's typed columns reach, written fresh.
//
// ColumnTypes.cpp gives every BTitledColumn a BNumberFormat, and BSizeColumn formats "1.5 KiB" with a BStringFormat. Haiku's
// real classes are built on ICU's i18n library, BLocaleRoster (a registrar, preference files, catalogs) and BResources; none
// of that exists here. These definitions satisfy the headers' own class layouts (so every Haiku file that includes them is
// compiled unchanged) with the "C" locale: no grouping separators, '.' as the decimal point, English plural rules, and the
// one pattern shape Haiku's size column uses, "{0, plural, one{... %s ...} other{... %s ...}}" (and plain "%s B"-style
// patterns, which are returned as they are). It is not a Locale Kit. A BLocale, a catalog or a date format is not provided.
#include <Format.h>
#include <FormattingConventions.h>
#include <Language.h>
#include <NumberFormat.h>
#include <StringFormat.h>
#include <String.h>

#include <stdio.h>
#include <string.h>

// ---- the base classes: constructed empty ------------------------------------------------------------------------------

BLanguage::BLanguage() : fICULocale(NULL) {}
BLanguage::BLanguage(const char*) : fICULocale(NULL) {}
BLanguage::BLanguage(const BLanguage&) : fICULocale(NULL) {}
BLanguage::~BLanguage() {}

BFormattingConventions::BFormattingConventions(const char*)
	: fUseStringsFromPreferredLanguage(false), fICULocale(NULL) {}
BFormattingConventions::~BFormattingConventions() {}
status_t BFormattingConventions::Archive(BMessage*, bool) const { return B_NOT_SUPPORTED; }

BFormat::BFormat(const BLocale*) : fInitStatus(B_OK) {}
BFormat::BFormat(const BFormat&) : fInitStatus(B_OK) {}
BFormat::~BFormat() {}

// ---- numbers ----------------------------------------------------------------------------------------------------------

class BNumberFormatImpl {
public:
	BNumberFormatImpl() : precision(-1) {}
	int precision;		// -1: shortest ("%g"-like) representation
};

BNumberFormat::BNumberFormat() : BFormat(), fPrivateData(new BNumberFormatImpl) {}
BNumberFormat::~BNumberFormat() { delete fPrivateData; }

status_t BNumberFormat::SetPrecision(int precision)
{
	fPrivateData->precision = precision;
	return B_OK;
}

status_t BNumberFormat::Format(BString& string, const double value)
{
	char buf[64];
	if (fPrivateData->precision >= 0)
		snprintf(buf, sizeof buf, "%.*f", fPrivateData->precision, value);
	else
		snprintf(buf, sizeof buf, "%g", value);
	string = buf;
	return B_OK;
}

status_t BNumberFormat::Format(BString& string, const int32 value)
{
	char buf[32];
	snprintf(buf, sizeof buf, "%d", (int)value);
	string = buf;
	return B_OK;
}

// ---- plural patterns --------------------------------------------------------------------------------------------------
// fFormatter is declared as an ICU MessageFormat*; here it points at the pattern, a heap BString.

BStringFormat::BStringFormat(const BString pattern) : BFormat(), fFormatter(reinterpret_cast<icu::MessageFormat*>(new BString(pattern))) {}
BStringFormat::~BStringFormat() { delete reinterpret_cast<BString*>(fFormatter); }
status_t BStringFormat::InitCheck() { return B_OK; }

// The text of the branch `{one{...}` / `{other{...}` of a "{0, plural, ...}" pattern, braces balanced; false if absent.
static bool Branch(const BString& pattern, const char* name, BString* out)
{
	BString key; key << name << "{";
	int32 at = pattern.FindFirst(key);
	if (at < 0)
		return false;
	int32 start = at + key.Length(), depth = 1, i = start;
	for (; i < pattern.Length() && depth > 0; i++) {
		if (pattern[i] == '{') depth++;
		else if (pattern[i] == '}') depth--;
	}
	if (depth != 0)
		return false;
	pattern.CopyInto(*out, start, i - 1 - start);
	return true;
}

status_t BStringFormat::Format(BString& buffer, const int64 arg) const
{
	const BString& pattern = *reinterpret_cast<const BString*>(fFormatter);
	BString body = pattern;
	if (pattern.FindFirst("plural,") >= 0) {
		BString branch;
		if (!(arg == 1 && Branch(pattern, "one", &branch)) && !Branch(pattern, "other", &branch))
			return B_BAD_VALUE;
		body = branch;
	}
	char number[32];
	snprintf(number, sizeof number, "%lld", (long long)arg);
	body.ReplaceAll("#", number);
	buffer = body;
	return B_OK;
}
