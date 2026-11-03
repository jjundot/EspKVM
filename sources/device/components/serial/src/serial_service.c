#include "serial_service.h"
#include "config_store.h"
#include "ek_crypto.h"
#include "ek_versions.h"
#include "ek_build_version.h"
#include "hid_service.h"
#include "driver/uart.h"
#include "esp_mac.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "frame.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "pb_decode.h"
#include "pb_encode.h"
#include <stdio.h>
static const char *TAG = "ek_serial";
static serial_parser parser;
static ek_SerialPacket request, response;
static void clear_request(void) {
    volatile unsigned char *p = (volatile unsigned char *)&request;
    for (size_t i = 0; i < sizeof(request); i++) p[i] = 0;
}
static uint8_t rx[128], tx[528];
static void request_received(uint32_t seq, const uint8_t *body, size_t n, void *ctx) {
    (void)ctx;
    request = (ek_SerialPacket)ek_SerialPacket_init_zero;
    pb_istream_t input = pb_istream_from_buffer(body, n);
    if (!pb_decode(&input, ek_SerialPacket_fields, &request) || !request.which_body) {
        clear_request();
        return;
    }
    response = (ek_SerialPacket)ek_SerialPacket_init_zero;
    response.proto_ver = EK_PROTO_VERSION;
    response.which_body = ek_SerialPacket_ack_tag;
    bool reboot = false;
    if (request.proto_ver != EK_PROTO_VERSION)
        response.body.ack.code = 1;
    else
        switch (request.which_body) {
        case ek_SerialPacket_info_get_tag:
            response.which_body = ek_SerialPacket_info_tag;
            response.body.info.fw_ver = EK_FW_VERSION;
            snprintf(response.body.info.fw_version, sizeof(response.body.info.fw_version), "%s", EK_FW_VERSION_STRING);
            response.body.info.has_config = true;
            response.body.info.paired = config_get(&response.body.info.config, true);
            ESP_LOGI(TAG, "UART info request: paired=%u", (unsigned)response.body.info.paired);
            response.body.info.mac.size = 6;
            esp_read_mac(response.body.info.mac.bytes, ESP_MAC_WIFI_STA);
            break;
        case ek_SerialPacket_cfg_set_tag:
            response.which_body = ek_SerialPacket_cfg_result_tag;
            response.body.cfg_result = config_save(&request.body.cfg_set);
            reboot = response.body.cfg_result.code == 0;
            if (reboot)
                ESP_LOGI(TAG, "UART configuration accepted; NVS commit succeeded, restart scheduled");
            else
                ESP_LOGW(TAG, "UART configuration rejected: code=%u field=%u",
                         response.body.cfg_result.code, response.body.cfg_result.field);
            break;
        case ek_SerialPacket_reboot_tag:
            ESP_LOGI(TAG, "UART reboot requested");
            reboot = true;
            break;
        case ek_SerialPacket_factory_reset_tag: {
            esp_err_t e = config_reset();
            response.body.ack.code = e == ESP_OK ? 0 : e == ESP_ERR_TIMEOUT ? 4 : 3;
            reboot = e == ESP_OK;
            break;
        }
        default:
            response.body.ack.code = 2;
            break;
        }
    if (reboot) hid_release();
    clear_request();
    pb_ostream_t output = pb_ostream_from_buffer(tx + 12, 512);
    if (!pb_encode(&output, ek_SerialPacket_fields, &response))
        return;
    frame_header header = {.len = (uint16_t)output.bytes_written, .flags = 2, .seq = seq};
    if (!frame_header_encode(tx, &header))
        return;
    size_t total = 12 + output.bytes_written;
    uint32_t crc = frame_crc32(tx, total);
    for (unsigned i = 0; i < 4; i++)
        tx[total + i] = (uint8_t)(crc >> (8 * i));
    int written = uart_write_bytes(UART_NUM_0, tx, total + 4);
    esp_err_t tx_result = uart_wait_tx_done(UART_NUM_0, pdMS_TO_TICKS(200));
    if (reboot) {
        if (written != (int)(total + 4) || tx_result != ESP_OK)
            ESP_LOGW(TAG, "reply transmission incomplete before restart: written=%d expected=%u wait=%s",
                     written, (unsigned)(total + 4), esp_err_to_name(tx_result));
        ESP_LOGI(TAG, "restarting after UART response");
        esp_restart();
    }
}
static void task(void *ctx) {
    (void)ctx;
    for (;;) {
        int n = uart_read_bytes(UART_NUM_0, rx, sizeof(rx), pdMS_TO_TICKS(20));
        serial_parser_feed(&parser, rx, n > 0 ? (size_t)n : 0,
                           (uint64_t)(esp_timer_get_time() / 1000), request_received, NULL);
        ek_zero(rx,sizeof(rx));
    }
}
// The UART driver is installed by serial_log_start, before any application log.
esp_err_t serial_service_start(void) {
    if (xTaskCreate(task, "serial", 4096, NULL, 5, NULL) != pdPASS)
        return ESP_ERR_NO_MEM;
    ESP_LOGI(TAG, "UART provisioning ready at 115200 baud");
    return ESP_OK;
}
