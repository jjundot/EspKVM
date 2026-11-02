idf_component_register(SRCS src/network_service.c INCLUDE_DIRS include
 REQUIRES protocol config hid_service esp_wifi esp_netif esp_event esp_timer esp_system lwip)
