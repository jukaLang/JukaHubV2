cmake_minimum_required(VERSION 3.10)

# moonlight-stream, JukaHub's Moonlight (GameStream) helper: a project of its
# own, apart from the chiaki-ng build (../CMakeLists.txt), so a problem here can
# never cost the PS Remote Play helpers their build. build-helpers.sh configures
# it after chiaki-stream and chiaki-regist have passed their checks:
#
#   cmake -S player/native/moonlight -B BUILD -G Ninja \
#     -DMOONLIGHT_COMMON_SOURCE=/path/to/moonlight-common-c \
#     -DBUILD_SHARED_LIBS=OFF -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
#     -DOPENSSL_USE_STATIC_LIBS=TRUE -DCMAKE_PREFIX_PATH=DEPS_PREFIX
#
# Linked like chiaki-stream: moonlight-common-c (with its enet and the
# distribution's static libcrypto), FFmpeg's avcodec, swscale and avutil and
# libopus from static archives in DEPS_PREFIX (pkg-config --static lists each
# chain in link order), and SDL2 for its headers and soname only: the handheld
# loads its own libSDL2-2.0.so.0.
#
# moonlight-common-c is GPLv3, so the moonlight-stream binary is GPLv3 as well
# (JukaHub itself is AGPL-3.0, which may be combined with it).
project(jukahub-moonlight C)

if(NOT MOONLIGHT_COMMON_SOURCE OR NOT EXISTS "${MOONLIGHT_COMMON_SOURCE}/CMakeLists.txt")
  message(FATAL_ERROR "MOONLIGHT_COMMON_SOURCE must name a moonlight-common-c checkout (with its enet submodule)")
endif()
get_filename_component(JUKAHUB_NATIVE_DIR "${CMAKE_CURRENT_SOURCE_DIR}/.." ABSOLUTE)

find_package(PkgConfig REQUIRED)
find_package(Threads REQUIRED)
pkg_check_modules(ML_SDL2 REQUIRED sdl2 IMPORTED_TARGET)
pkg_check_modules(ML_FFMPEG REQUIRED libavcodec libswscale libavutil)
pkg_check_modules(ML_OPUS REQUIRED opus)

# The way moonlight-embedded consumes it: its own CMakeLists.txt builds the
# static moonlight-common-c target (and enet beside it).
add_subdirectory("${MOONLIGHT_COMMON_SOURCE}" "${CMAKE_BINARY_DIR}/moonlight-common-c" EXCLUDE_FROM_ALL)
if(NOT TARGET moonlight-common-c)
  message(FATAL_ERROR "${MOONLIGHT_COMMON_SOURCE} defines no moonlight-common-c target")
endif()
# A warning in a newer compiler than upstream's CI must not stop this build:
# -Wno-error after whatever options the library sets wins on the command line.
foreach(ml_target moonlight-common-c enet)
  if(TARGET ${ml_target})
    set_property(TARGET ${ml_target} APPEND PROPERTY COMPILE_OPTIONS -Wno-error)
  endif()
endforeach()

add_executable(moonlight-stream
  ${JUKAHUB_NATIVE_DIR}/moonlight-stream.c
  ${JUKAHUB_NATIVE_DIR}/stream_input.c
  ${JUKAHUB_NATIVE_DIR}/stream_osd.c
  ${JUKAHUB_NATIVE_DIR}/stream_sys.c)
target_compile_options(moonlight-stream PRIVATE -Wall -Wextra -Wpedantic)
# The self-test clip (../README.md, "Self-test"), built in with .incbin: its
# absolute path, quoted, and only when the file exists, so a checkout without
# it still builds (the self-test then says decoding was not checked).
# OBJECT_DEPENDS rebuilds moonlight-stream.c when the clip changes: the
# compiler's dependency list never sees a file the assembler includes.
set(ML_SELFTEST_CLIP_FILE "${JUKAHUB_NATIVE_DIR}/moonlight-selftest.h264")
if(EXISTS "${ML_SELFTEST_CLIP_FILE}")
  target_compile_definitions(moonlight-stream PRIVATE "ML_SELFTEST_CLIP=\"${ML_SELFTEST_CLIP_FILE}\"")
  set_source_files_properties("${JUKAHUB_NATIVE_DIR}/moonlight-stream.c" PROPERTIES
    OBJECT_DEPENDS "${ML_SELFTEST_CLIP_FILE}")
else()
  message(WARNING "${ML_SELFTEST_CLIP_FILE} is missing: the moonlight-stream self-test will not check decoding")
endif()
# Limelight.h's folder is named here too, whatever the library exports.
target_include_directories(moonlight-stream PRIVATE
  ${JUKAHUB_NATIVE_DIR}
  "${MOONLIGHT_COMMON_SOURCE}/src"
  ${ML_FFMPEG_STATIC_INCLUDE_DIRS} ${ML_FFMPEG_INCLUDE_DIRS}
  ${ML_OPUS_STATIC_INCLUDE_DIRS} ${ML_OPUS_INCLUDE_DIRS})
# libm for the OSD and the pointer maths, libdl for SDL_JoystickPath (looked
# up at run time, SDL 2.24) and the static libcrypto, threads for the connect
# thread and every static archive that uses them.
target_link_libraries(moonlight-stream PRIVATE
  moonlight-common-c
  ${ML_FFMPEG_STATIC_LDFLAGS}
  ${ML_OPUS_STATIC_LDFLAGS}
  PkgConfig::ML_SDL2
  m ${CMAKE_DL_LIBS} Threads::Threads)
# C_EXTENSIONS keeps gnu11: under a strict -std=c11 glibc's signal() is the
# one-shot System V flavour, and a second SIGTERM would kill the stream before
# it writes its status file (as for chiaki-stream).
set_target_properties(moonlight-stream PROPERTIES
  RUNTIME_OUTPUT_DIRECTORY "${CMAKE_CURRENT_BINARY_DIR}"
  C_STANDARD 11
  C_STANDARD_REQUIRED ON
  C_EXTENSIONS ON)

install(TARGETS moonlight-stream RUNTIME DESTINATION bin)
