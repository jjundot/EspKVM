cmake_minimum_required(VERSION 3.24)
project(EspKVM LANGUAGES C)
include(CTest)
option(BUILD_DEVICE "Enable ESP-IDF firmware targets" OFF)
option(BUILD_HOST "Enable Go host targets" ON)
if(WIN32)
 set(managed_python "${CMAKE_SOURCE_DIR}/build/tools/proto/Scripts/python.exe")
else()
 set(managed_python "${CMAKE_SOURCE_DIR}/build/tools/proto/bin/python")
endif()
if(NOT DEFINED Python3_EXECUTABLE AND EXISTS "${managed_python}")
 set(Python3_EXECUTABLE "${managed_python}" CACHE FILEPATH "Protocol generator Python")
endif()
find_package(Python3 REQUIRED COMPONENTS Interpreter)
add_subdirectory(sources/protocol)
if(BUILD_HOST)
find_program(GO_EXECUTABLE go REQUIRED)
add_custom_target(windows-input-probe
 COMMAND "${CMAKE_COMMAND}" -E env GOOS=windows GOARCH=amd64 CGO_ENABLED=0
  "${GO_EXECUTABLE}" build -trimpath -o "${CMAKE_SOURCE_DIR}/build/host/bin/windows/inputprobe.exe"
  "${CMAKE_SOURCE_DIR}/experiments/windows-input/main.go"
 VERBATIM)
add_custom_target(host ALL
 COMMAND "${CMAKE_COMMAND}" -E env GOOS=linux GOARCH=amd64 CGO_ENABLED=0
  "${GO_EXECUTABLE}" build -trimpath -o "${CMAKE_SOURCE_DIR}/build/host/bin/linux/" ./cmd/...
 COMMAND "${CMAKE_COMMAND}" -E env GOOS=windows GOARCH=amd64 CGO_ENABLED=0
  "${GO_EXECUTABLE}" build -trimpath -o "${CMAKE_SOURCE_DIR}/build/host/bin/windows/" ./cmd/...
 WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/host" DEPENDS proto-go VERBATIM)
add_custom_target(test-host
 COMMAND "${GO_EXECUTABLE}" test -race ./...
 WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/host" DEPENDS proto-go VERBATIM)
endif()
add_executable(protocol-test sources/device/components/protocol/src/frame.c sources/device/components/protocol/test/test_frame.c)
target_include_directories(protocol-test PRIVATE sources/device/components/protocol/include)
target_compile_features(protocol-test PRIVATE c_std_11)
add_test(NAME protocol-vectors COMMAND protocol-test "${CMAKE_SOURCE_DIR}/sources/protocol/testdata/frames.txt")
add_custom_target(test-protocol COMMAND ${CMAKE_CTEST_COMMAND} --output-on-failure DEPENDS protocol-test VERBATIM)
add_executable(hid-reports-test sources/device/components/hid_reports/src/hid_reports.c
 sources/device/components/hid_reports/test/reports_test.c)
target_include_directories(hid-reports-test PRIVATE sources/device/components/hid_reports/include)
target_compile_features(hid-reports-test PRIVATE c_std_11)
add_test(NAME hid-reports COMMAND hid-reports-test)
add_dependencies(test-protocol hid-reports-test)
add_executable(hid-engine-test sources/device/components/hid_reports/src/hid_reports.c
 sources/device/components/hid_reports/src/hid_engine.c sources/device/components/hid_reports/test/engine_test.c)
target_include_directories(hid-engine-test PRIVATE sources/device/components/hid_reports/include)
add_test(NAME hid-engine COMMAND hid-engine-test)
add_dependencies(test-protocol hid-engine-test)
add_executable(protocol-random-test sources/device/components/protocol/src/frame.c sources/device/components/protocol/test/test_fuzz.c)
target_include_directories(protocol-random-test PRIVATE sources/device/components/protocol/include)
add_test(NAME protocol-randomized COMMAND protocol-random-test)
add_dependencies(test-protocol protocol-random-test)
if(CMAKE_C_COMPILER_ID MATCHES "Clang")
 add_executable(protocol-fuzz EXCLUDE_FROM_ALL sources/device/components/protocol/src/frame.c sources/device/components/protocol/test/test_fuzz.c)
 target_include_directories(protocol-fuzz PRIVATE sources/device/components/protocol/include)
 target_compile_definitions(protocol-fuzz PRIVATE EK_LIBFUZZER)
 target_compile_options(protocol-fuzz PRIVATE -fsanitize=fuzzer,address,undefined)
 target_link_options(protocol-fuzz PRIVATE -fsanitize=fuzzer,address,undefined)
endif()
option(EK_SANITIZERS "Instrument portable C tests with address/undefined sanitizers" OFF)
if(EK_SANITIZERS)
 foreach(test_target protocol-test hid-reports-test hid-engine-test protocol-random-test)
  target_compile_options(${test_target} PRIVATE -fsanitize=address,undefined -fno-omit-frame-pointer)
  target_link_options(${test_target} PRIVATE -fsanitize=address,undefined)
 endforeach()
endif()
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
 add_executable(linux-input-probe EXCLUDE_FROM_ALL experiments/linux-input/probe.c)
 target_compile_features(linux-input-probe PRIVATE c_std_11)
 target_compile_options(linux-input-probe PRIVATE -Wall -Wextra -Werror)
endif()
include(cmake/ProtocolVectors.cmake)
foreach(test_target protocol-test hid-reports-test hid-engine-test protocol-random-test protocol-v2-test config-store-test network-native-server protocol-fuzz)
 if(TARGET ${test_target})
  if(MSVC)
   target_compile_options(${test_target} PRIVATE /UNDEBUG)
  else()
   target_compile_options(${test_target} PRIVATE -UNDEBUG)
  endif()
 endif()
endforeach()
if(EK_SANITIZERS)
 foreach(test_target protocol-v2-test config-store-test network-native-server)
  if(TARGET ${test_target})
   target_compile_options(${test_target} PRIVATE -fsanitize=address,undefined -fno-omit-frame-pointer)
   target_link_options(${test_target} PRIVATE -fsanitize=address,undefined)
  endif()
 endforeach()
endif()
if(BUILD_DEVICE)
 find_program(IDF_PY idf.py REQUIRED)
 add_custom_target(device ALL COMMAND "${IDF_PY}" -B "${CMAKE_SOURCE_DIR}/build/device/firmware"
  -D "Python3_EXECUTABLE=${Python3_EXECUTABLE}" build
  WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/device" USES_TERMINAL VERBATIM)
 add_custom_target(proto-device COMMAND "${IDF_PY}" -B "${CMAKE_SOURCE_DIR}/build/device/firmware"
  -D "Python3_EXECUTABLE=${Python3_EXECUTABLE}" reconfigure
  COMMAND "${CMAKE_COMMAND}" --build "${CMAKE_SOURCE_DIR}/build/device/firmware" --target proto-c
  WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/sources/device" USES_TERMINAL VERBATIM)
 add_dependencies(proto proto-device)
 add_dependencies(device proto-device)
 foreach(target hid-selftest rebuild-hid-selftest)
  if(target STREQUAL "rebuild-hid-selftest")
   set(clean_action COMMAND "${IDF_PY}" -B "${CMAKE_SOURCE_DIR}/build/device/hid-selftest" fullclean)
  else()
   set(clean_action)
  endif()
  add_custom_target(${target} ${clean_action}
   COMMAND "${IDF_PY}" -B "${CMAKE_SOURCE_DIR}/build/device/hid-selftest" build
   WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}/experiments/usb-hid" USES_TERMINAL VERBATIM)
 endforeach()
 add_custom_target(package-hid-selftest
  COMMAND "${Python3_EXECUTABLE}" "${CMAKE_SOURCE_DIR}/tools/package_device.py"
   "${CMAKE_SOURCE_DIR}/build/device/hid-selftest" "${CMAKE_SOURCE_DIR}/build/device/flash/hid-selftest" --auto
  DEPENDS hid-selftest VERBATIM)
 add_custom_target(package-device
  COMMAND "${Python3_EXECUTABLE}" "${CMAKE_SOURCE_DIR}/tools/package_device.py"
   "${CMAKE_SOURCE_DIR}/build/device/firmware" "${CMAKE_SOURCE_DIR}/build/device/flash/firmware" --auto
  DEPENDS device VERBATIM)
endif()
