idf_component_register(SRCS src/config_store.c INCLUDE_DIRS include REQUIRES protocol nvs_flash)
add_dependencies(${COMPONENT_LIB} proto-c)
