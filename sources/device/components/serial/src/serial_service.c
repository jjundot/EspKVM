#include "serial_service.h"
#include "config_store.h"
#include "driver/uart.h"
#include "esp_mac.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "frame.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "pb_decode.h"
#include "pb_encode.h"
static serial_parser parser;
static ek_SerialPacket request, response;
static uint8_t rx[128], tx[528];
static void request_received(uint32_t seq, const uint8_t *body, size_t n, void *ctx) {
    (void)ctx;
    request = (ek_SerialPacket)ek_SerialPacket_init_zero;
    pb_istream_t input = pb_istream_from_buffer(body, n);
    if (!pb_decode(&input, ek_SerialPacket_fields, &request) || !request.which_body)
        return;
    response = (ek_SerialPacket)ek_SerialPacket_init_zero;
    response.proto_ver = 1;
    response.which_body = ek_SerialPacket_ack_tag;
    bool reboot = false;
    if (request.proto_ver != 1)
        response.body.ack.code = 1;
    else
        switch (request.which_body) {
        case ek_SerialPacket_info_get_tag:
            response.which_body = ek_SerialPacket_info_tag;
            response.body.info.fw_ver = 0x000100;
            response.body.info.has_config = true;
            response.body.info.paired = config_get(&response.body.info.config, true);
            esp_read_mac(response.body.info.mac, ESP_MAC_WIFI_STA);
            break;
        case ek_SerialPacket_cfg_set_tag:
            response.which_body = ek_SerialPacket_cfg_result_tag;
            response.body.cfg_result = config_save(&request.body.cfg_set);
            reboot = response.body.cfg_result.code == 0;
            break;
        case ek_SerialPacket_reboot_tag:
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
    uart_write_bytes(UART_NUM_0, tx, total + 4);
    uart_wait_tx_done(UART_NUM_0, pdMS_TO_TICKS(200));
    if (reboot)
        esp_restart();
}
static void task(void *ctx) {
    (void)ctx;
    for (;;) {
        int n = uart_read_bytes(UART_NUM_0, rx, sizeof(rx), pdMS_TO_TICKS(20));
        serial_parser_feed(&parser, rx, n > 0 ? (size_t)n : 0,
                           (uint64_t)(esp_timer_get_time() / 1000), request_received, NULL);
    }
}
esp_err_t serial_service_start(void) {
    uart_config_t config = {.baud_rate = 115200,
                            .data_bits = UART_DATA_8_BITS,
                            .parity = UART_PARITY_DISABLE,
                            .stop_bits = UART_STOP_BITS_1,
                            .flow_ctrl = UART_HW_FLOWCTRL_DISABLE,
                            .source_clk = UART_SCLK_DEFAULT};
    esp_err_t e = uart_param_config(UART_NUM_0, &config);
    if (e != ESP_OK)
        return e;
    if ((e = uart_driver_install(UART_NUM_0, 1024, 1024, 0, NULL, 0)) != ESP_OK)
        return e;
    return xTaskCreate(task, "serial", 4096, NULL, 5, NULL) == pdPASS ? ESP_OK : ESP_ERR_NO_MEM;
}
