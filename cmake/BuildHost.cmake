cmake_minimum_required(VERSION 3.24)

foreach(required GO_EXECUTABLE SOURCE_ROOT HOST_SOURCE_DIR OUTPUT_DIR EK_GIT_SHA1)
 if(NOT DEFINED ${required} OR "${${required}}" STREQUAL "")
  message(FATAL_ERROR "BuildHost.cmake requires ${required}")
 endif()
endforeach()

string(LENGTH "${EK_GIT_SHA1}" git_sha_length)
if(NOT git_sha_length EQUAL 40 OR NOT EK_GIT_SHA1 MATCHES "^[0-9a-fA-F]+$")
 message(FATAL_ERROR "EK_GIT_SHA1 must be a full 40-character SHA-1")
endif()

get_filename_component(source_root "${SOURCE_ROOT}" REALPATH)
file(MAKE_DIRECTORY "${OUTPUT_DIR}")
get_filename_component(output_dir "${OUTPUT_DIR}" REALPATH)
if(NOT output_dir STREQUAL "${source_root}/build/host/bin")
 message(FATAL_ERROR "Refusing to clean unexpected host output directory: ${output_dir}")
endif()

execute_process(COMMAND git -C "${source_root}" rev-parse HEAD
 OUTPUT_VARIABLE current_git_sha1 OUTPUT_STRIP_TRAILING_WHITESPACE RESULT_VARIABLE git_result)
if(git_result EQUAL 0)
 set(EK_GIT_SHA1 "${current_git_sha1}")
endif()
string(LENGTH "${EK_GIT_SHA1}" git_sha_length)
if(NOT git_sha_length EQUAL 40 OR NOT EK_GIT_SHA1 MATCHES "^[0-9a-fA-F]+$")
 message(FATAL_ERROR "Could not resolve a full Git SHA-1 for this host build")
endif()

string(TIMESTAMP build_timestamp "%Y%m%dT%H%M%SZ" UTC)
string(SUBSTRING "${EK_GIT_SHA1}" 0 7 short_git_sha)
set(app_version "${short_git_sha}_${build_timestamp}")
message(STATUS "Building EspKVM host version ${app_version}")

# Remove the previous platform-specific layout; the current release artifacts
# live together in build/host/bin with architecture encoded in each filename.
file(REMOVE_RECURSE "${output_dir}/linux" "${output_dir}/windows")
file(REMOVE
 "${output_dir}/espkvm.exe"
 "${output_dir}/espkvm"
 "${output_dir}/espkvmctl.exe"
 "${output_dir}/espkvmctl"
 "${output_dir}/espkvm_x86_64_linux.exe"
 "${output_dir}/espkvm_aarch64_linux.exe"
)

function(build_host goos goarch filename)
 execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env
   "GOOS=${goos}" "GOARCH=${goarch}" "CGO_ENABLED=0"
   "${GO_EXECUTABLE}" build -trimpath
   "-ldflags=-X=main.version=${app_version}"
   -o "${output_dir}/${filename}" ./cmd/espkvm
  WORKING_DIRECTORY "${HOST_SOURCE_DIR}"
  RESULT_VARIABLE result
 )
 if(NOT result EQUAL 0)
  message(FATAL_ERROR "Failed to build ${filename} (exit ${result})")
 endif()
endfunction()

build_host(windows amd64 espkvm_x86_64_windows.exe)
build_host(linux amd64 espkvm_x86_64_linux)
build_host(linux arm64 espkvm_aarch64_linux)
