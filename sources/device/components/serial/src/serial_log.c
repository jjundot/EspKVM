#include "serial_log.h"
#include "driver/uart.h"
#include "ek.pb.h"
#include "ek_versions.h"
#include "esp_log.h"
#include "frame.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "pb_encode.h"
#include <stdio.h>
#include <string.h>

// Application logs share the provisioning UART only as complete CRC frames.
// Each frame is one uart_write_bytes call, which the driver serializes against
// provisioning replies, so text can never split a reply frame.
static StaticSemaphore_t mutex_storage;
static SemaphoreHandle_t mutex;
static ek_SerialPacket packet;
static uint8_t frame[528];
static uint32_t sequence;
static int log_vprintf(const char *format, va_list args) {
    if (xSemaphoreTake(mutex, pdMS_TO_TICKS(20)) != pdTRUE)
        return 0;
    packet = (ek_SerialPacket)ek_SerialPacket_init_zero;
    packet.proto_ver = EK_PROTO_VERSION;
    packet.which_body = ek_SerialPacket_log_tag;
    char *text = packet.body.log.text;
    int n = vsnprintf(text, sizeof(packet.body.log.text), format, args);
    size_t size = strnlen(text, sizeof(packet.body.log.text));
    while (size && (text[size - 1] == '\n' || text[size - 1] == '\r'))
        text[--size] = 0;
    pb_ostream_t output = pb_ostream_from_buffer(frame + 12, 512);
    if (++sequence == 0)
        sequence = 1;
    frame_header header = {.flags = EK_FLAG_CRC, .seq = sequence};
    if (size && pb_encode(&output, ek_SerialPacket_fields, &packet)) {
        header.len = (uint16_t)output.bytes_written;
        if (frame_header_encode(frame, &header)) {
            size_t total = 12 + output.bytes_written;
            uint32_t crc = frame_crc32(frame, total);
            for (unsigned i = 0; i < 4; i++)
                frame[total + i] = (uint8_t)(crc >> (8 * i));
            uart_write_bytes(UART_NUM_0, frame, total + 4);
        }
    }
    xSemaphoreGive(mutex);
    return n;
}
esp_err_t serial_log_start(void) {
    uart_config_t config = {.baud_rate = 115200,
                            .data_bits = UART_DATA_8_BITS,
                            .parity = UART_PARITY_DISABLE,
                            .stop_bits = UART_STOP_BITS_1,
                            .flow_ctrl = UART_HW_FLOWCTRL_DISABLE,
                            .source_clk = UART_SCLK_DEFAULT};
    esp_err_t e = uart_param_config(UART_NUM_0, &config);
    if (e != ESP_OK)
        return e;
    if ((e = uart_driver_install(UART_NUM_0, 1024, 2048, 0, NULL, 0)) != ESP_OK)
        return e;
    mutex = xSemaphoreCreateMutexStatic(&mutex_storage);
    esp_log_set_vprintf(log_vprintf);
    return ESP_OK;
}
