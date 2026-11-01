cmake_minimum_required(VERSION 3.24)
project(EspKVM LANGUAGES C)
include(CTest)
option(BUILD_DEVICE "Enable ESP-IDF firmware targets" OFF)
option(BUILD_HOST "Enable Go host targets" ON)
find_package(Python3 REQUIRED COMPONENTS Interpreter)
include(cmake/Protocol.cmake)
add_custom_target(proto)
if(BUILD_HOST)
find_program(GO_EXECUTABLE go REQUIRED)
generate_go_protocol()
add_custom_target(host
 COMMAND "${GO_EXECUTABLE}" build -trimpath -o "${CMAKE_SOURCE_DIR}/out/bin/" ./cmd/...
 WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/host" DEPENDS proto-go VERBATIM)
add_dependencies(proto proto-go)
add_custom_target(test-host
 COMMAND "${GO_EXECUTABLE}" test -race ./...
 WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/host" DEPENDS proto-go VERBATIM)
endif()
add_executable(protocol-test sources/device/components/protocol/src/frame.c sources/device/components/protocol/test/test_frame.c)
target_include_directories(protocol-test PRIVATE sources/device/components/protocol/include)
target_compile_features(protocol-test PRIVATE c_std_11)
add_test(NAME protocol-vectors COMMAND protocol-test "${CMAKE_SOURCE_DIR}/sources/protocol/testdata/frames.txt")
add_custom_target(test-protocol COMMAND ${CMAKE_CTEST_COMMAND} --output-on-failure DEPENDS protocol-test VERBATIM)
if(BUILD_DEVICE)
 find_program(IDF_PY idf.py REQUIRED)
 add_custom_target(device COMMAND "${IDF_PY}" -B "${CMAKE_SOURCE_DIR}/out/device"
  -D "Python3_EXECUTABLE=${Python3_EXECUTABLE}" build
  WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/device" USES_TERMINAL VERBATIM)
 add_custom_target(proto-device COMMAND "${IDF_PY}" -B "${CMAKE_SOURCE_DIR}/out/device"
  -D "Python3_EXECUTABLE=${Python3_EXECUTABLE}" reconfigure
  COMMAND "${CMAKE_COMMAND}" --build "${CMAKE_SOURCE_DIR}/out/device" --target proto-c
  WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/device" USES_TERMINAL VERBATIM)
 add_dependencies(proto proto-device)
 add_dependencies(device proto-device)
 add_custom_target(package-device
  COMMAND "${Python3_EXECUTABLE}" "${CMAKE_SOURCE_DIR}/tools/package_device.py"
   "${CMAKE_SOURCE_DIR}/out/device" "${CMAKE_SOURCE_DIR}/out/flash" --auto
  DEPENDS device VERBATIM)
endif()
