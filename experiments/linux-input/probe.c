#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <glob.h>
#include <linux/input.h>
#include <poll.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <time.h>
#include <unistd.h>
enum { MAX_DEVICES = 32, KEY_BYTES = (KEY_CNT + 7) / 8 };
typedef struct {
    int fd;
    char path[256];
    uint8_t keys[KEY_BYTES];
    bool dropped, grabbed;
} device;
static device devices[MAX_DEVICES];
static unsigned count, refs[KEY_CNT];
static uint64_t events, batches, dropped, added, removed;
static volatile sig_atomic_t stopping;
static bool remote;
static uint64_t now_ms(void) {
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return (uint64_t)t.tv_sec * 1000 + t.tv_nsec / 1000000;
}
static void stop(int sig) {
    (void)sig;
    stopping = 1;
}
static bool bit(const uint8_t *keys, unsigned key) { return (keys[key / 8] >> (key % 8)) & 1; }
static void update(device *d, const uint8_t *keys) {
    for (unsigned k = 0; k < KEY_CNT; k++) {
        if (bit(d->keys, k) && !bit(keys, k))
            refs[k]--;
        if (!bit(d->keys, k) && bit(keys, k))
            refs[k]++;
    }
    memcpy(d->keys, keys, KEY_BYTES);
}
static bool sync_keys(device *d) {
    uint8_t keys[KEY_BYTES] = {0};
    if (ioctl(d->fd, EVIOCGKEY(sizeof(keys)), keys) < 0)
        return false;
    update(d, keys);
    return true;
}
static bool released(void) {
    for (unsigned k = 0; k < KEY_CNT; k++)
        if (refs[k])
            return false;
    return true;
}
static void local(void) {
    for (unsigned i = 0; i < count; i++)
        if (devices[i].grabbed) {
            ioctl(devices[i].fd, EVIOCGRAB, 0);
            devices[i].grabbed = false;
        }
    if (remote)
        puts("mode=LOCAL");
    remote = false;
}
static bool grab(void) {
    if (!count || !released())
        return false;
    for (unsigned i = 0; i < count; i++) {
        if (ioctl(devices[i].fd, EVIOCGRAB, 1) < 0) {
            local();
            return false;
        }
        devices[i].grabbed = true;
    }
    // Grabs are not atomic across FDs: re-read after the entire set is held.
    for (unsigned i = 0; i < count; i++)
        if (!sync_keys(&devices[i])) {
            local();
            return false;
        }
    if (!released()) {
        local();
        return false;
    }
    remote = true;
    puts("mode=REMOTE");
    return true;
}
static void scan(void) {
    glob_t paths = {0};
    if (glob("/dev/input/event*", 0, NULL, &paths) != 0) {
        globfree(&paths);
        return;
    }
    for (unsigned i = 0; i < count;) {
        bool exists = false;
        for (size_t j = 0; j < paths.gl_pathc; j++)
            if (!strcmp(devices[i].path, paths.gl_pathv[j]))
                exists = true;
        if (!exists) {
            local();
            uint8_t empty[KEY_BYTES] = {0};
            update(&devices[i], empty);
            close(devices[i].fd);
            devices[i] = devices[--count];
            removed++;
        } else
            i++;
    }
    for (size_t i = 0; i < paths.gl_pathc && count < MAX_DEVICES; i++) {
        bool known = false;
        for (unsigned j = 0; j < count; j++)
            if (!strcmp(devices[j].path, paths.gl_pathv[i]))
                known = true;
        if (known)
            continue;
        int fd = open(paths.gl_pathv[i], O_RDONLY | O_NONBLOCK | O_CLOEXEC);
        if (fd < 0)
            continue;
        char name[128] = {0};
        ioctl(fd, EVIOCGNAME(sizeof(name)), name);
        if (!strncmp(name, "EspKVM", 6)) {
            close(fd);
            continue;
        }
        uint8_t capabilities[(EV_CNT + 7) / 8] = {0};
        if (ioctl(fd, EVIOCGBIT(0, sizeof(capabilities)), capabilities) < 0 ||
            !bit(capabilities, EV_KEY)) {
            close(fd);
            continue;
        }
        local();
        device *d = &devices[count++];
        *d = (device){.fd = fd};
        snprintf(d->path, sizeof(d->path), "%s", paths.gl_pathv[i]);
        if (!sync_keys(d)) {
            close(fd);
            count--;
            continue;
        }
        added++;
    }
    globfree(&paths);
}
int main(int argc, char **argv) {
    unsigned duration = 15, grab_seconds = 0;
    for (int i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "--duration") && i + 1 < argc)
            duration = (unsigned)atoi(argv[++i]);
        else if (!strcmp(argv[i], "--grab-seconds") && i + 1 < argc)
            grab_seconds = (unsigned)atoi(argv[++i]);
        else {
            fprintf(stderr, "usage: linux-input-probe [--duration 1..60] [--grab-seconds 1..2]\n");
            return 2;
        }
    }
    if (!duration || duration > 60 || grab_seconds > 2)
        return 2;
    struct sigaction action = {.sa_handler = stop};
    sigaction(SIGINT, &action, NULL);
    sigaction(SIGTERM, &action, NULL);
    setvbuf(stdout, NULL, _IOLBF, 0);
    puts("mode=LOCAL passive; only event counts are recorded");
    scan();
    if (!count) {
        fprintf(
            stderr,
            "no readable input devices; check input group permissions and a real Linux desktop\n");
        return 1;
    }
    uint64_t start = now_ms(), next_scan = start + 500, ungrab = 0;
    bool attempted = false;
    while (!stopping && now_ms() - start < duration * 1000) {
        uint64_t now = now_ms();
        if (now >= next_scan) {
            scan();
            next_scan = now + 500;
        }
        if (grab_seconds && !attempted && now - start >= 3000 && released()) {
            attempted = true;
            if (grab())
                ungrab = now + grab_seconds * 1000;
            else
                puts("grab failed: rolled back all FDs");
        }
        if (remote && now >= ungrab)
            local();
        struct pollfd fds[MAX_DEVICES];
        for (unsigned i = 0; i < count; i++)
            fds[i] = (struct pollfd){.fd = devices[i].fd, .events = POLLIN};
        if (poll(fds, count, 20) < 0 && errno != EINTR)
            break;
        for (unsigned i = 0; i < count; i++)
            if (fds[i].revents) {
                struct input_event batch[64];
                ssize_t bytes = read(devices[i].fd, batch, sizeof(batch));
                if (bytes < 0 && errno != EAGAIN && errno != EINTR) {
                    local();
                    continue;
                }
                if (bytes <= 0)
                    continue;
                for (size_t j = 0; j < (size_t)bytes / sizeof(batch[0]); j++) {
                    struct input_event *e = &batch[j];
                    events++;
                    if (e->type == EV_SYN && e->code == SYN_DROPPED) {
                        devices[i].dropped = true;
                        dropped++;
                        local();
                        continue;
                    }
                    if (e->type == EV_SYN && e->code == SYN_REPORT) {
                        batches++;
                        if (devices[i].dropped) {
                            sync_keys(&devices[i]);
                            devices[i].dropped = false;
                        }
                        continue;
                    }
                    if (!devices[i].dropped && e->type == EV_KEY && e->code < KEY_CNT &&
                        e->value != 2) {
                        uint8_t keys[KEY_BYTES];
                        memcpy(keys, devices[i].keys, sizeof(keys));
                        if (e->value)
                            keys[e->code / 8] |= 1u << (e->code % 8);
                        else
                            keys[e->code / 8] &= ~(1u << (e->code % 8));
                        update(&devices[i], keys);
                    }
                }
            }
    }
    local();
    for (unsigned i = 0; i < count; i++)
        close(devices[i].fd);
    printf("devices=%u events=%llu batches=%llu syn_dropped=%llu added=%llu removed=%llu\n", count,
           (unsigned long long)events, (unsigned long long)batches, (unsigned long long)dropped,
           (unsigned long long)added, (unsigned long long)removed);
    return 0;
}
