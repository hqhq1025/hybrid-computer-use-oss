#ifndef COMPUTER_USE_INPUT_QUEUE_H
#define COMPUTER_USE_INPUT_QUEUE_H
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <stddef.h>

#define INPUT_QUEUE_MAGIC 0x31514943U
#define INPUT_QUEUE_VERSION 1U
#define INPUT_QUEUE_MAX_EVENTS 4096U
#define INPUT_QUEUE_NAME_CHARS 128U

typedef struct {
    DWORD magic, version, target_pid, target_tid, sender_pid, count, map_bytes;
    volatile LONG enabled, active, installed, install_error, cleanup_error, completed;
    DWORD reserved[3];
} InputQueueHeader;

typedef struct {
    DWORD tag, vk, key_up;
    volatile LONG consumed_sequence;
    DWORD state_ok, modifier_down_mask, actual_message, reserved;
} InputQueueRecord;

_Static_assert(sizeof(InputQueueHeader) == 64, "input queue header ABI");
_Static_assert(offsetof(InputQueueHeader, enabled) == 28, "enabled ABI");
_Static_assert(offsetof(InputQueueHeader, active) == 32, "active ABI");
_Static_assert(offsetof(InputQueueHeader, installed) == 36, "installed ABI");
_Static_assert(offsetof(InputQueueHeader, install_error) == 40, "install error ABI");
_Static_assert(offsetof(InputQueueHeader, cleanup_error) == 44, "cleanup error ABI");
_Static_assert(offsetof(InputQueueHeader, completed) == 48, "completed ABI");
_Static_assert(sizeof(InputQueueRecord) == 32, "input queue record ABI");
_Static_assert(offsetof(InputQueueRecord, consumed_sequence) == 12, "sequence ABI");
_Static_assert(offsetof(InputQueueRecord, state_ok) == 16, "state ABI");

static BOOL iq_append(WCHAR *text, DWORD capacity, const WCHAR *suffix) {
    DWORD used = 0;
    while (used < capacity && text[used]) used++;
    if (used == capacity) return FALSE;
    while (*suffix && used + 1 < capacity) text[used++] = *suffix++;
    text[used] = 0;
    return !*suffix;
}

static BOOL iq_append_number(WCHAR *text, DWORD capacity, DWORD number) {
    WCHAR reverse[10], forward[11];
    DWORD count = 0, index = 0;
    do { reverse[count++] = (WCHAR)(L'0' + number % 10); number /= 10; } while (number);
    while (count) forward[index++] = reverse[--count];
    forward[index] = 0;
    return iq_append(text, capacity, forward);
}

static BOOL iq_name(WCHAR *name, DWORD pid, DWORD tid, const WCHAR *suffix) {
    name[0] = 0;
    return iq_append(name, INPUT_QUEUE_NAME_CHARS, L"Local\\ComputerUse.InputQueue.") &&
        iq_append_number(name, INPUT_QUEUE_NAME_CHARS, pid) &&
        iq_append(name, INPUT_QUEUE_NAME_CHARS, L".") &&
        iq_append_number(name, INPUT_QUEUE_NAME_CHARS, tid) &&
        iq_append(name, INPUT_QUEUE_NAME_CHARS, suffix);
}

static BOOL iq_valid_header(const InputQueueHeader *header, DWORD pid, DWORD tid) {
    return header->magic == INPUT_QUEUE_MAGIC && header->version == INPUT_QUEUE_VERSION &&
        header->target_pid == pid && header->target_tid == tid && header->sender_pid &&
        header->count && header->count <= INPUT_QUEUE_MAX_EVENTS &&
        header->map_bytes == sizeof(InputQueueHeader) + header->count * sizeof(InputQueueRecord);
}

static void iq_error(volatile LONG *destination, DWORD error) {
    InterlockedCompareExchange(destination, (LONG)(error ? error : ERROR_GEN_FAILURE), 0);
}

static DWORD iq_last_error(void) {
    DWORD error = GetLastError();
    return error ? error : ERROR_GEN_FAILURE;
}

#endif
