idf_component_register(SRCS app_main.c usb_device.c INCLUDE_DIRS .
    REQUIRES hid_reports nvs_flash esp_driver_gpio esp_timer esp_tinyusb)
