#include "input_queue.h"

#if defined(_M_IX86)
#pragma comment(linker, "/export:InputQueueGetMessage=_InputQueueGetMessage@12")
#else
#pragma comment(linker, "/export:InputQueueGetMessage")
#endif

/* The callback owns its mapping handles. One DLL can observe several target
   threads without sharing a process-global pointer to one thread's queue. */
LRESULT CALLBACK InputQueueGetMessage(int code, WPARAM removal, LPARAM parameter) {
    if (code == HC_ACTION && (removal & PM_REMOVE)) {
        const MSG *message = (const MSG *)parameter;
        BOOL down = message->message == WM_KEYDOWN || message->message == WM_SYSKEYDOWN;
        BOOL up = message->message == WM_KEYUP || message->message == WM_SYSKEYUP;
        if (down || up) {
            ULONG_PTR extra_info = (ULONG_PTR)GetMessageExtraInfo();
            DWORD tag = extra_info <= MAXDWORD ? (DWORD)extra_info : 0;
            DWORD pid = GetCurrentProcessId(), tid = GetCurrentThreadId();
            WCHAR name[INPUT_QUEUE_NAME_CHARS];
            HANDLE mapping = NULL;
            InputQueueHeader *header = NULL;
            DWORD map_bytes = 0;
            if (tag && iq_name(name, pid, tid, L""))
                mapping = OpenFileMappingW(FILE_MAP_READ | FILE_MAP_WRITE, FALSE, name);
            if (mapping)
                header = (InputQueueHeader *)MapViewOfFile(mapping,
                    FILE_MAP_READ | FILE_MAP_WRITE, 0, 0, sizeof(InputQueueHeader));
            if (header) {
                if (iq_valid_header(header, pid, tid)) map_bytes = header->map_bytes;
                UnmapViewOfFile(header);
                header = NULL;
            }
            if (map_bytes)
                header = (InputQueueHeader *)MapViewOfFile(mapping,
                    FILE_MAP_READ | FILE_MAP_WRITE, 0, 0, map_bytes);
            if (header) {
                InterlockedIncrement(&header->active);
                if (iq_valid_header(header, pid, tid) && header->map_bytes == map_bytes &&
                    InterlockedCompareExchange(&header->enabled, 0, 0)) {
                    InputQueueRecord *records = (InputQueueRecord *)(header + 1);
                    DWORD index;
                    for (index = 0; index < header->count; index++) {
                        InputQueueRecord *record = &records[index];
                        if (record->tag == tag && record->vk == (DWORD)message->wParam &&
                            record->key_up == (DWORD)up &&
                            InterlockedCompareExchange(&record->consumed_sequence, -1, 0) == 0) {
                            const BYTE modifiers[] = {
                                VK_CONTROL, VK_LCONTROL, VK_RCONTROL,
                                VK_SHIFT, VK_LSHIFT, VK_RSHIFT,
                                VK_MENU, VK_LMENU, VK_RMENU, VK_LWIN, VK_RWIN
                            };
                            BYTE state[256];
                            DWORD bit, mask = 0;
                            BOOL state_ok = GetKeyboardState(state);
                            HANDLE changed;
                            LONG sequence;
                            if (state_ok) {
                                for (bit = 0; bit < sizeof(modifiers); bit++)
                                    if (state[modifiers[bit]] & 0x80) mask |= 1U << bit;
                            }
                            record->state_ok = state_ok ? 1U : 0U;
                            record->modifier_down_mask = mask;
                            record->actual_message = message->message;
                            sequence = InterlockedIncrement(&header->completed);
                            InterlockedExchange(&record->consumed_sequence, sequence);
                            iq_name(name, pid, tid, L".Changed");
                            changed = OpenEventW(EVENT_MODIFY_STATE, FALSE, name);
                            if (changed) {
                                if (!SetEvent(changed)) iq_error(&header->cleanup_error, GetLastError());
                                CloseHandle(changed);
                            } else iq_error(&header->cleanup_error, GetLastError());
                            break;
                        }
                    }
                }
                InterlockedDecrement(&header->active);
                UnmapViewOfFile(header);
            }
            if (mapping) CloseHandle(mapping);
        }
    }
    return CallNextHookEx(NULL, code, removal, parameter);
}

BOOL WINAPI DllMain(HINSTANCE instance, DWORD reason, LPVOID reserved) {
    (void)reserved;
    if (reason == DLL_PROCESS_ATTACH) DisableThreadLibraryCalls(instance);
    return TRUE;
}
