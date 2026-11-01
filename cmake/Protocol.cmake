set(PROJECT_ROOT "${CMAKE_CURRENT_LIST_DIR}/..")
get_filename_component(PROJECT_ROOT "${PROJECT_ROOT}" ABSOLUTE)
function(generate_go_protocol)
 find_program(PROTOC_GEN_GO protoc-gen-go HINTS "${PROJECT_ROOT}/build/tools/bin" REQUIRED)
 set(output "${PROJECT_ROOT}/sources/host/internal/protocol/pb/ek.pb.go")
 set(stamp "${CMAKE_BINARY_DIR}/proto-go.stamp")
 add_custom_command(OUTPUT "${stamp}" BYPRODUCTS "${output}"
  COMMAND "${Python3_EXECUTABLE}" "${PROJECT_ROOT}/tools/generate.py" go "${output}" "${PROTOC_GEN_GO}"
  COMMAND "${CMAKE_COMMAND}" -E touch "${stamp}"
  DEPENDS "${PROJECT_ROOT}/sources/protocol/ek.proto" "${PROJECT_ROOT}/sources/protocol/ek.options"
   "${PROJECT_ROOT}/tools/generate.py" "${PROJECT_ROOT}/tools/versions.env" "${PROTOC_GEN_GO}"
   "${Python3_EXECUTABLE}" "${CMAKE_CURRENT_FUNCTION_LIST_FILE}"
  VERBATIM)
 add_custom_target(proto-go DEPENDS "${stamp}" "${output}")
endfunction()
function(generate_c_protocol output_dir)
 find_package(Python3 REQUIRED COMPONENTS Interpreter)
 get_filename_component(python_bin "${Python3_EXECUTABLE}" DIRECTORY)
 find_program(NANOPB_PLUGIN protoc-gen-nanopb HINTS "${python_bin}" REQUIRED)
 set(stamp "${output_dir}/proto-c.stamp")
 add_custom_command(OUTPUT "${stamp}" BYPRODUCTS "${output_dir}/ek.pb.c" "${output_dir}/ek.pb.h"
  COMMAND "${Python3_EXECUTABLE}" "${PROJECT_ROOT}/tools/generate.py" c "${output_dir}" "${NANOPB_PLUGIN}"
  COMMAND "${CMAKE_COMMAND}" -E touch "${stamp}"
  DEPENDS "${PROJECT_ROOT}/sources/protocol/ek.proto" "${PROJECT_ROOT}/sources/protocol/ek.options"
   "${PROJECT_ROOT}/tools/generate.py" "${PROJECT_ROOT}/tools/versions.env" "${NANOPB_PLUGIN}"
   "${CMAKE_CURRENT_FUNCTION_LIST_FILE}" VERBATIM)
 add_custom_target(proto-c DEPENDS "${stamp}" "${output_dir}/ek.pb.c" "${output_dir}/ek.pb.h")
endfunction()
