idf_component_register(SRCS src/serial_service.c src/serial_log.c INCLUDE_DIRS include REQUIRES hid_service protocol config esp_driver_uart esp_timer esp_hw_support log)
add_dependencies(${COMPONENT_LIB} proto-c)
