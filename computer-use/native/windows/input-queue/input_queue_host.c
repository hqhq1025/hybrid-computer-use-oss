#include "input_queue.h"
#include <shellapi.h>

/* No CRT is linked into the helper. */
void *memset(void *target, int value, size_t count) {
    BYTE *bytes = (BYTE *)target;
    while (count--) *bytes++ = (BYTE)value;
    return target;
}
void *memcpy(void *target, const void *source, size_t count) {
    BYTE *output = (BYTE *)target;
    const BYTE *input = (const BYTE *)source;
    while (count--) *output++ = *input++;
    return target;
}

static BOOL equal(const WCHAR *left, const WCHAR *right) {
    while (*left && *left == *right) { left++; right++; }
    return *left == *right;
}

static BOOL number(const WCHAR *text, DWORD *value) {
    DWORD parsed = 0;
    if (!*text) return FALSE;
    while (*text) {
        DWORD digit;
        if (*text < L'0' || *text > L'9') return FALSE;
        digit = (DWORD)(*text++ - L'0');
        if (parsed > (MAXDWORD - digit) / 10) return FALSE;
        parsed = parsed * 10 + digit;
    }
    *value = parsed;
    return parsed != 0;
}

static BOOL same_architecture(HANDLE target) {
    USHORT own_process, own_native, target_process, target_native;
    if (!IsWow64Process2(GetCurrentProcess(), &own_process, &own_native) ||
        !IsWow64Process2(target, &target_process, &target_native)) return FALSE;
    return (own_process ? own_process : own_native) ==
        (target_process ? target_process : target_native);
}

static BOOL same_session(DWORD pid) {
    DWORD target, own;
    return ProcessIdToSessionId(pid, &target) &&
        ProcessIdToSessionId(GetCurrentProcessId(), &own) && target == own;
}

void WINAPI mainCRTStartup(void) {
    int argc = 0, index;
    WCHAR **argv = CommandLineToArgvW(GetCommandLineW(), &argc);
    WCHAR name[INPUT_QUEUE_NAME_CHARS], dll_path[1024];
    DWORD pid = 0, tid = 0, error = 0, size = 0;
    HANDLE mapping = NULL, ready = NULL, stop = NULL;
    HANDLE target = NULL, sender = NULL, target_thread = NULL;
    HMODULE dll = NULL;
    HHOOK hook = NULL;
    InputQueueHeader *header = NULL;
    BOOL installed = FALSE;

    if (!argv || argc != 5) { error = ERROR_BAD_ARGUMENTS; goto done; }
    for (index = 1; index < argc; index += 2) {
        if (equal(argv[index], L"--pid") && !pid) {
            if (!number(argv[index + 1], &pid)) { error = ERROR_BAD_ARGUMENTS; goto done; }
        } else if (equal(argv[index], L"--tid") && !tid) {
            if (!number(argv[index + 1], &tid)) { error = ERROR_BAD_ARGUMENTS; goto done; }
        } else { error = ERROR_BAD_ARGUMENTS; goto done; }
    }
    if (!pid || !tid || !iq_name(name, pid, tid, L"")) { error = ERROR_BAD_ARGUMENTS; goto done; }
    iq_name(name, pid, tid, L".Ready");
    ready = OpenEventW(EVENT_MODIFY_STATE, FALSE, name);
    iq_name(name, pid, tid, L".Stop");
    stop = OpenEventW(SYNCHRONIZE, FALSE, name);
    if (!ready || !stop) { error = ERROR_INVALID_HANDLE; goto done; }
    iq_name(name, pid, tid, L"");
    mapping = OpenFileMappingW(FILE_MAP_READ | FILE_MAP_WRITE, FALSE, name);
    if (!mapping) { error = iq_last_error(); goto done; }
    header = (InputQueueHeader *)MapViewOfFile(mapping, FILE_MAP_READ | FILE_MAP_WRITE,
        0, 0, sizeof(InputQueueHeader));
    if (!header) { error = iq_last_error(); goto done; }
    if (!iq_valid_header(header, pid, tid)) { error = ERROR_INVALID_DATA; goto done; }
    size = header->map_bytes;
    UnmapViewOfFile(header);
    header = (InputQueueHeader *)MapViewOfFile(mapping, FILE_MAP_READ | FILE_MAP_WRITE, 0, 0, size);
    if (!header) { error = iq_last_error(); goto done; }
    if (!iq_valid_header(header, pid, tid) || header->map_bytes != size) { error = ERROR_INVALID_DATA; goto done; }
    {
        HANDLE changed;
        iq_name(name, pid, tid, L".Changed");
        changed = OpenEventW(EVENT_MODIFY_STATE, FALSE, name);
        if (!changed) { error = iq_last_error(); goto done; }
        CloseHandle(changed);
    }
    target = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE, FALSE, pid);
    sender = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE, FALSE, header->sender_pid);
    target_thread = OpenThread(THREAD_QUERY_LIMITED_INFORMATION | SYNCHRONIZE, FALSE, tid);
    if (!target || !sender || !target_thread) { error = iq_last_error(); goto done; }
    if (GetProcessIdOfThread(target_thread) != pid || !same_session(pid) ||
        !same_session(header->sender_pid) || !same_architecture(target) ||
        WaitForSingleObject(target, 0) != WAIT_TIMEOUT ||
        WaitForSingleObject(sender, 0) != WAIT_TIMEOUT ||
        WaitForSingleObject(target_thread, 0) != WAIT_TIMEOUT) {
        error = ERROR_INVALID_PARAMETER; goto done;
    }
    {
        InputQueueRecord *records = (InputQueueRecord *)(header + 1);
        DWORD item, prior;
        for (item = 0; item < header->count; item++) {
            if (!records[item].tag || !records[item].vk || records[item].vk > 254 ||
                records[item].key_up > 1 || records[item].consumed_sequence) {
                error = ERROR_INVALID_DATA; goto done;
            }
            for (prior = 0; prior < item; prior++) {
                if (records[prior].tag == records[item].tag) { error = ERROR_INVALID_DATA; goto done; }
            }
        }
    }
    size = GetModuleFileNameW(NULL, dll_path, (DWORD)(sizeof(dll_path) / sizeof(dll_path[0])));
    if (!size || size >= sizeof(dll_path) / sizeof(dll_path[0]) || size < 4 ||
        !equal(dll_path + size - 4, L".exe")) { error = ERROR_BAD_PATHNAME; goto done; }
    dll_path[size - 3] = L'd'; dll_path[size - 2] = L'l'; dll_path[size - 1] = L'l';
    dll = LoadLibraryW(dll_path);
    if (!dll) { error = iq_last_error(); goto done; }
    {
        HOOKPROC callback = (HOOKPROC)GetProcAddress(dll, "InputQueueGetMessage");
        if (!callback) { error = ERROR_PROC_NOT_FOUND; goto done; }
        hook = SetWindowsHookExW(WH_GETMESSAGE, callback, dll, tid);
    }
    if (!hook) { error = iq_last_error(); goto done; }
    installed = TRUE;
    InterlockedExchange(&header->enabled, 1);
    InterlockedExchange(&header->installed, 1);
    if (!SetEvent(ready)) { error = iq_last_error(); goto done; }
    {
        HANDLE waits[] = {stop, target, sender, target_thread};
        DWORD result = WaitForMultipleObjects(4, waits, FALSE, 30000);
        if (result == WAIT_TIMEOUT) error = ERROR_TIMEOUT;
        else if (result == WAIT_FAILED) error = iq_last_error();
        else if (result != WAIT_OBJECT_0) error = ERROR_PROCESS_ABORTED;
    }
done:
    if (header) {
        if (error) iq_error(installed ? &header->cleanup_error : &header->install_error, error);
        InterlockedExchange(&header->enabled, 0);
    }
    if (!installed && ready) SetEvent(ready);
    if (hook && !UnhookWindowsHookEx(hook)) {
        error = iq_last_error();
        if (header) iq_error(&header->cleanup_error, error);
    }
    if (header) {
        ULONGLONG deadline = GetTickCount64() + 1000;
        while (InterlockedCompareExchange(&header->active, 0, 0) && GetTickCount64() < deadline) Sleep(1);
        if (InterlockedCompareExchange(&header->active, 0, 0)) {
            iq_error(&header->cleanup_error, ERROR_TIMEOUT);
            error = ERROR_TIMEOUT;
        }
        if (header->install_error || header->cleanup_error) error = error ? error : ERROR_GEN_FAILURE;
        UnmapViewOfFile(header);
    }
    if (mapping) CloseHandle(mapping);
    if (ready) CloseHandle(ready);
    if (stop) CloseHandle(stop);
    if (target_thread) CloseHandle(target_thread);
    if (target) CloseHandle(target);
    if (sender) CloseHandle(sender);
    if (argv) LocalFree(argv);
    /* Do not explicitly unload a callback image while an unhooked callback
       can still finish. Target-side callbacks own their mapping views. */
    ExitProcess(error ? 1 : 0);
}
