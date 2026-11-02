idf_component_register(SRCS src/serial_service.c INCLUDE_DIRS include REQUIRES protocol config esp_driver_uart esp_timer esp_hw_support)
add_dependencies(${COMPONENT_LIB} proto-c)
