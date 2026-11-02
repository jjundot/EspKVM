#pragma once
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/random.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <time.h>
#include <unistd.h>
#define pdPASS 1
#define portMAX_DELAY 0
#define configMAX_PRIORITIES 25
#define pdMS_TO_TICKS(n) (n)
typedef pthread_mutex_t StaticSemaphore_t;
typedef pthread_mutex_t *SemaphoreHandle_t;
static inline SemaphoreHandle_t xSemaphoreCreateMutexStatic(StaticSemaphore_t *m) {
    pthread_mutex_init(m, NULL);
    return m;
}
static inline void xSemaphoreTake(SemaphoreHandle_t m, int wait) {
    (void)wait;
    pthread_mutex_lock(m);
}
static inline void xSemaphoreGive(SemaphoreHandle_t m) { pthread_mutex_unlock(m); }
static inline int64_t esp_timer_get_time(void) {
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return (int64_t)t.tv_sec * 1000000 + t.tv_nsec / 1000;
}
static inline void esp_fill_random(void *out, size_t n) {
    if (getrandom(out, n, 0) != (ssize_t)n)
        abort();
}
static inline void esp_restart(void) { exit(0); }
static inline void vTaskDelay(unsigned ms) {
    struct timespec t = {.tv_sec = ms / 1000, .tv_nsec = (ms % 1000) * 1000000};
    nanosleep(&t, NULL);
}
static inline void vTaskDelete(void *unused) {
    (void)unused;
    pthread_exit(NULL);
}
typedef struct {
    void (*fn)(void *);
    void *arg;
} host_task;
static inline void *host_task_entry(void *arg) {
    host_task task = *(host_task *)arg;
    free(arg);
    task.fn(task.arg);
    return NULL;
}
static inline int xTaskCreate(void (*fn)(void *), const char *name, unsigned stack, void *arg,
                              unsigned priority, void *handle) {
    (void)name;
    (void)stack;
    (void)priority;
    (void)handle;
    host_task *task = malloc(sizeof(*task));
    if (!task)
        return 0;
    *task = (host_task){fn, arg};
    pthread_t thread;
    if (pthread_create(&thread, NULL, host_task_entry, task)) {
        free(task);
        return 0;
    }
    pthread_detach(thread);
    return pdPASS;
}
typedef struct {
    int rssi;
} wifi_ap_record_t;
static inline int esp_wifi_sta_get_ap_info(wifi_ap_record_t *record) {
    record->rssi = -42;
    return 0;
}
