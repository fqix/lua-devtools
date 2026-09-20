/* Optional Lua ABI-neutral stdin readiness module. No Lua headers or liblua.
 * Only these four API functions are used; all have the same signatures in
 * Lua 5.1-5.5 and LuaJIT. No pseudo-indices, numbers or Lua internals are used.
 */
typedef struct lua_State lua_State;
typedef int (*lua_CFunction)(lua_State *);

#ifdef _WIN32
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <psapi.h>
#define EXPORT __declspec(dllexport)
static void (*lua_createtable)(lua_State *, int, int);
static void (*lua_pushcclosure)(lua_State *, lua_CFunction, int);
static void (*lua_setfield)(lua_State *, int, const char *);
static void (*lua_pushboolean)(lua_State *, int);

static int resolve_api(void) {
    DWORD needed = 0;
    HMODULE *modules;
    DWORD i;
    int found = 0;
    if (!EnumProcessModules(GetCurrentProcess(), NULL, 0, &needed) || !needed)
        return 0;
    modules = (HMODULE *)HeapAlloc(GetProcessHeap(), 0, needed);
    if (!modules) return 0;
    {
        DWORD capacity = needed;
        if (!EnumProcessModules(GetCurrentProcess(), modules, capacity, &needed) || needed > capacity) {
            HeapFree(GetProcessHeap(), 0, modules);
            return 0;
        }
    }
    for (i = 0; i < needed / sizeof(HMODULE); ++i) {
        FARPROC a = GetProcAddress(modules[i], "lua_createtable");
        FARPROC b = GetProcAddress(modules[i], "lua_pushcclosure");
        FARPROC c = GetProcAddress(modules[i], "lua_setfield");
        FARPROC d = GetProcAddress(modules[i], "lua_pushboolean");
        if (!(a && b && c && d)) continue;
        /* Multiple Lua providers are ambiguous: do not risk using the wrong VM. */
        if (found) { found = 0; break; }
        lua_createtable = (void (*)(lua_State *, int, int))a;
        lua_pushcclosure = (void (*)(lua_State *, lua_CFunction, int))b;
        lua_setfield = (void (*)(lua_State *, int, const char *))c;
        lua_pushboolean = (void (*)(lua_State *, int))d;
        found = 1;
    }
    HeapFree(GetProcessHeap(), 0, modules);
    return found;
}
static int stdin_ready(void) {
    DWORD available = 0;
    HANDLE input = GetStdHandle(STD_INPUT_HANDLE);
    if (PeekNamedPipe(input, NULL, 0, NULL, &available, NULL)) return available > 0;
    /* EOF must wake the reader so a disconnected adapter cannot leave a loop running. */
    return GetLastError() == ERROR_BROKEN_PIPE;
}
#else
#include <poll.h>
#include <unistd.h>
#define EXPORT __attribute__((visibility("default")))
extern void lua_createtable(lua_State *, int, int);
extern void lua_pushcclosure(lua_State *, lua_CFunction, int);
extern void lua_setfield(lua_State *, int, const char *);
extern void lua_pushboolean(lua_State *, int);
static int stdin_ready(void) {
    struct pollfd input = { STDIN_FILENO, POLLIN, 0 };
    return poll(&input, 1, 0) > 0 && (input.revents & (POLLIN | POLLHUP));
}
#endif

static int poll_stdin(lua_State *L) {
    lua_pushboolean(L, stdin_ready());
    return 1;
}

EXPORT int luaopen_lua_devtools_native(lua_State *L) {
#ifdef _WIN32
    /* Returning no module is a safe load failure, even when no Lua API is found. */
    if (!resolve_api()) return 0;
#endif
    lua_createtable(L, 0, 1);
    lua_pushcclosure(L, poll_stdin, 0);
    lua_setfield(L, -2, "poll_stdin");
    return 1;
}
