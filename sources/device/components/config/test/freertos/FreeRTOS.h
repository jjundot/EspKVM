#pragma once
#include <pthread.h>
#define pdTRUE 1
#define portMAX_DELAY 0xffffffffu
typedef pthread_mutex_t StaticSemaphore_t;
typedef pthread_mutex_t *SemaphoreHandle_t;
static inline SemaphoreHandle_t xSemaphoreCreateMutexStatic(StaticSemaphore_t *m) { *m=(pthread_mutex_t)PTHREAD_MUTEX_INITIALIZER;return m; }
static inline int xSemaphoreTake(SemaphoreHandle_t m,unsigned wait) {return (wait?pthread_mutex_lock(m):pthread_mutex_trylock(m))==0;}
static inline void xSemaphoreGive(SemaphoreHandle_t m) {pthread_mutex_unlock(m);}
