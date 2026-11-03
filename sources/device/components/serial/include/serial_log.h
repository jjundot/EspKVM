#pragma once
#include "esp_err.h"
// Installs the UART0 driver and frames every ESP_LOG line as SerialPacket.log.
esp_err_t serial_log_start(void);
