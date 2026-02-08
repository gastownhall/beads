// This code has been adapted from https://github.com/mysql/mysql-server/blob/ea7087d885006918ad54458e7aad215b1650312c/sql/regexp/regexp_engine.cc

// #cgo macos CFLAGS: -Ibin/darwin-aarch64/include
// #cgo macos CXXFLAGS: -Ibin/darwin-aarch64/include

#include "unicode/regex.h"
#include "cstring"

#ifdef __cplusplus
extern "C" {
#endif

void appendHead(URegularExpression* m_re, std::u16string& m_replace_buffer, int& m_replace_buffer_pos, UErrorCode* m_error_code, size_t size) {
	if (size == 0) { return; }
	int32_t text_length32 = 0;
	auto text = uregex_getText(m_re, &text_length32, m_error_code);
	if (*m_error_code != U_ZERO_ERROR) { return; }
	if (m_replace_buffer.size() < size) { m_replace_buffer.resize(size); }
	std::copy(text, text + size, &m_replace_buffer.at(0));
	m_replace_buffer_pos = (int)size;
}

int tryToAppendReplacement(URegularExpression* m_re, std::u16string& m_replace_buffer, int& m_replace_buffer_pos, UErrorCode* m_error_code, UChar* replacement, int replacementLen) {
	if (m_replace_buffer.empty()) { return 0; }
	UChar* ptr = reinterpret_cast<UChar*>(&m_replace_buffer.at(0) + m_replace_buffer_pos);
	int capacity = m_replace_buffer.size() - m_replace_buffer_pos;
	return uregex_appendReplacement(m_re, replacement, replacementLen, &ptr, &capacity, m_error_code);
}

void appendReplacement(URegularExpression* m_re, std::u16string& m_replace_buffer, int& m_replace_buffer_pos, UErrorCode* m_error_code, UChar* replacement, int replacementLen) {
	int replacement_size = tryToAppendReplacement(m_re, m_replace_buffer, m_replace_buffer_pos, m_error_code, replacement, replacementLen);
	if (*m_error_code == U_BUFFER_OVERFLOW_ERROR) {
		size_t required_buffer_size = m_replace_buffer_pos + replacement_size;
		m_replace_buffer.resize(required_buffer_size);
		*m_error_code = U_ZERO_ERROR;
		tryToAppendReplacement(m_re, m_replace_buffer, m_replace_buffer_pos, m_error_code, replacement, replacementLen);
	}
	m_replace_buffer_pos += replacement_size;
}

int tryToAppendTail(URegularExpression* m_re, std::u16string& m_replace_buffer, int& m_replace_buffer_pos, UErrorCode* m_error_code) {
	if (m_replace_buffer.empty()) { return 0; }
	UChar* ptr = reinterpret_cast<UChar*>(&m_replace_buffer.at(0) + m_replace_buffer_pos);
	int capacity = m_replace_buffer.size() - m_replace_buffer_pos;
	return uregex_appendTail(m_re, &ptr, &capacity, m_error_code);
}

void appendTail(URegularExpression* m_re, std::u16string& m_replace_buffer, int& m_replace_buffer_pos, UErrorCode* m_error_code) {
	int tail_size = tryToAppendTail(m_re, m_replace_buffer, m_replace_buffer_pos, m_error_code);
	if (*m_error_code == U_BUFFER_OVERFLOW_ERROR) {
		size_t required_buffer_size = m_replace_buffer_pos + tail_size;
		m_replace_buffer.resize(required_buffer_size);
		*m_error_code = U_ZERO_ERROR;
		tryToAppendTail(m_re, m_replace_buffer, m_replace_buffer_pos, m_error_code);
	}
	m_replace_buffer_pos += tail_size;
}

UChar* replace(URegularExpression* regexp, UChar* replacement, int replacementLen, UChar* original, int originalSize, int start, int occurrence, int* returnSize) {
	*returnSize = originalSize;
	UErrorCode m_error_code = U_ZERO_ERROR;
	std::u16string m_replace_buffer;
	int m_replace_buffer_pos = 0;
	bool found = uregex_find(regexp, start, &m_error_code);
	int end_of_previous_match = 0;
	for (int i = 1; i < occurrence && found; ++i) {
		end_of_previous_match = uregex_end(regexp, 0, &m_error_code);
		found = uregex_findNext(regexp, &m_error_code);
	}
	if (!found && U_SUCCESS(m_error_code)) { return original; }
	m_replace_buffer.resize(originalSize);
	appendHead(regexp, m_replace_buffer, m_replace_buffer_pos, &m_error_code, std::max(end_of_previous_match, start));
	if (found) {
		do {
			appendReplacement(regexp, m_replace_buffer, m_replace_buffer_pos, &m_error_code, replacement, replacementLen);
		} while (occurrence == 0 && uregex_findNext(regexp, &m_error_code));
	}
	appendTail(regexp, m_replace_buffer, m_replace_buffer_pos, &m_error_code);
	m_replace_buffer.resize(m_replace_buffer_pos);
	UChar* returnStr = static_cast<UChar*>(malloc(m_replace_buffer.size() * sizeof(UChar*)));
	memcpy(returnStr, m_replace_buffer.data(), m_replace_buffer.size() * sizeof(UChar*));
	*returnSize = m_replace_buffer.size();
	return returnStr;
}

#ifdef __cplusplus
} // extern "C"
#endif
