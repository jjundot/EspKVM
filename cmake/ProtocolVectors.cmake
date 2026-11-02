# Native tests use the same pinned PSA implementation as ESP-IDF firmware.
include("${CMAKE_CURRENT_LIST_DIR}/Protocol.cmake")
set(EK_PSA_SOURCE "" CACHE PATH "ESP-IDF mbedtls/tf-psa-crypto source directory")
set(EK_NANOPB_SOURCE "${PROJECT_ROOT}/build/device/deps/nanopb" CACHE PATH "Pinned nanopb runtime")
if(EK_PSA_SOURCE)
 set(ENABLE_PROGRAMS OFF CACHE BOOL "" FORCE)
 set(ENABLE_TESTING OFF CACHE BOOL "" FORCE)
 set(GEN_FILES OFF CACHE BOOL "" FORCE)
 set(TF_PSA_CRYPTO_CONFIG_FILE "${PROJECT_ROOT}/sources/device/components/protocol/test/psa_config.h" CACHE FILEPATH "" FORCE)
 add_subdirectory("${EK_PSA_SOURCE}" "${CMAKE_BINARY_DIR}/psa" EXCLUDE_FROM_ALL)
 generate_c_protocol("${CMAKE_BINARY_DIR}/generated/protocol")
 add_executable(protocol-v2-test
  "${PROJECT_ROOT}/sources/device/components/protocol/src/frame.c"
  "${PROJECT_ROOT}/sources/device/components/protocol/src/ek_crypto.c"
  "${PROJECT_ROOT}/sources/device/components/protocol/src/message_validate.c"
  "${PROJECT_ROOT}/sources/device/components/protocol/test/test_v2.c"
  "${CMAKE_BINARY_DIR}/generated/protocol/ek.pb.c"
  "${EK_NANOPB_SOURCE}/pb_common.c" "${EK_NANOPB_SOURCE}/pb_decode.c" "${EK_NANOPB_SOURCE}/pb_encode.c")
 target_include_directories(protocol-v2-test PRIVATE "${PROJECT_ROOT}/sources/device/components/protocol/include"
  "${CMAKE_BINARY_DIR}/generated/protocol" "${EK_NANOPB_SOURCE}")
 target_link_libraries(protocol-v2-test PRIVATE tfpsacrypto)
 add_dependencies(protocol-v2-test proto-c)
 add_dependencies(test-protocol protocol-v2-test)
 add_test(NAME protocol-v2 COMMAND protocol-v2-test "${PROJECT_ROOT}/sources/protocol/testdata/v2.txt")
 if(UNIX)
  add_executable(config-store-test
   "${PROJECT_ROOT}/sources/device/components/config/src/config_store.c"
   "${PROJECT_ROOT}/sources/device/components/config/test/store_test.c"
   "${PROJECT_ROOT}/sources/device/components/protocol/src/frame.c"
   "${CMAKE_BINARY_DIR}/generated/protocol/ek.pb.c"
   "${EK_NANOPB_SOURCE}/pb_common.c" "${EK_NANOPB_SOURCE}/pb_decode.c" "${EK_NANOPB_SOURCE}/pb_encode.c")
  target_include_directories(config-store-test PRIVATE
   "${PROJECT_ROOT}/sources/device/components/config/test"
   "${PROJECT_ROOT}/sources/device/components/config/include"
   "${PROJECT_ROOT}/sources/device/components/network/test"
   "${PROJECT_ROOT}/sources/device/components/protocol/include"
   "${CMAKE_BINARY_DIR}/generated/protocol" "${EK_NANOPB_SOURCE}")
  target_link_libraries(config-store-test PRIVATE pthread)
  add_dependencies(config-store-test proto-c)
  add_dependencies(test-protocol config-store-test)
  add_test(NAME config-store COMMAND config-store-test)
  add_executable(network-native-server
   "${PROJECT_ROOT}/sources/device/components/network/src/network_service.c"
   "${PROJECT_ROOT}/sources/device/components/network/test/server.c"
   "${PROJECT_ROOT}/sources/device/components/protocol/src/frame.c"
   "${PROJECT_ROOT}/sources/device/components/protocol/src/ek_crypto.c"
   "${PROJECT_ROOT}/sources/device/components/protocol/src/message_validate.c"
   "${CMAKE_BINARY_DIR}/generated/protocol/ek.pb.c"
   "${EK_NANOPB_SOURCE}/pb_common.c" "${EK_NANOPB_SOURCE}/pb_decode.c" "${EK_NANOPB_SOURCE}/pb_encode.c")
  target_compile_definitions(network-native-server PRIVATE EK_NETWORK_HOST_TEST)
  target_include_directories(network-native-server PRIVATE
   "${PROJECT_ROOT}/sources/device/components/network/test"
   "${PROJECT_ROOT}/sources/device/components/network/include"
   "${PROJECT_ROOT}/sources/device/components/hid_service/include"
   "${PROJECT_ROOT}/sources/device/components/config/include"
   "${PROJECT_ROOT}/sources/device/components/protocol/include"
   "${CMAKE_BINARY_DIR}/generated/protocol" "${EK_NANOPB_SOURCE}")
  target_link_libraries(network-native-server PRIVATE tfpsacrypto pthread)
  add_dependencies(network-native-server proto-c)
  if(BUILD_HOST)
   add_dependencies(test-protocol network-native-server proto-go)
   add_test(NAME firmware-interop COMMAND "${CMAKE_COMMAND}" -E env
    "ESPKVM_NETWORK_TEST_SERVER=$<TARGET_FILE:network-native-server>"
    "${GO_EXECUTABLE}" test -race ./internal/session ./internal/devices -run TestFirmware -count=1)
   set_tests_properties(firmware-interop PROPERTIES WORKING_DIRECTORY "${PROJECT_ROOT}/sources/host")
  endif()
 endif()
endif()
