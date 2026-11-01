# Protocol generation belongs to the normal build graph. Consumers depend on
# these targets so a clean parallel build cannot compile before code generation.
include("${CMAKE_CURRENT_LIST_DIR}/../../cmake/Protocol.cmake")

add_custom_target(proto ALL)
if(BUILD_HOST)
    generate_go_protocol()
    add_dependencies(proto proto-go)
endif()
# Firmware C generation is registered by the IDF protocol component in its own
# binary directory. The root proto-device target delegates to that producer.
