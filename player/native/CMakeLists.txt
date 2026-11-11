cmake_minimum_required(VERSION 3.10)

# JukaHub's two Remote Play helpers. build-helpers.sh adds this directory to a
# pinned chiaki-ng checkout (an add_subdirectory appended to chiaki-ng's own
# CMakeLists.txt), so chiaki-lib and its include paths already exist here.
#
# JUKAHUB_STATIC_DEPS is the release build (build-helpers.sh native mode):
# FFmpeg's swscale, avcodec and avutil are static archives in a private prefix.
# A static archive only resolves what is still undefined when the linker reaches
# it, and the plain pkg-config target names libswscale alone, so nothing put
# avutil after it; pkg-config --static lists the whole chain in link order,
# right after chiaki-lib, which needs avcodec and avutil itself.
option(JUKAHUB_STATIC_DEPS "Link FFmpeg from static archives (release builds)" OFF)

find_package(PkgConfig REQUIRED)
find_package(Threads REQUIRED)
pkg_check_modules(TRIMUI_SDL2 REQUIRED sdl2 IMPORTED_TARGET)

if(JUKAHUB_STATIC_DEPS)
  pkg_check_modules(TRIMUI_FFMPEG REQUIRED libswscale libavcodec libavutil)
  set(JUKAHUB_FFMPEG_LINK ${TRIMUI_FFMPEG_STATIC_LDFLAGS})
  set(JUKAHUB_FFMPEG_INCLUDE ${TRIMUI_FFMPEG_STATIC_INCLUDE_DIRS} ${TRIMUI_FFMPEG_INCLUDE_DIRS})
else()
  pkg_check_modules(TRIMUI_SWSCALE REQUIRED libswscale IMPORTED_TARGET)
  set(JUKAHUB_FFMPEG_LINK PkgConfig::TRIMUI_SWSCALE)
  set(JUKAHUB_FFMPEG_INCLUDE "")
endif()

# libm for the OSD and audio maths; libdl and threads for the static archives
# that call into them (OpenSSL's and chiaki-lib's) and for chiaki-stream itself,
# which looks SDL_JoystickPath (SDL 2.24) up with dlsym so an older firmware SDL
# still loads it. The SDL 2.0.14 touchpad events are compiled in against newer
# headers and acted on only when the running SDL reports 2.0.14 or later.
set(JUKAHUB_SYSTEM_LIBS m ${CMAKE_DL_LIBS} Threads::Threads)

add_executable(chiaki-stream chiaki-stream.c stream_input.c stream_osd.c stream_sys.c)
target_compile_features(chiaki-stream PRIVATE c_std_11)
target_compile_options(chiaki-stream PRIVATE -Wall -Wextra -Wpedantic)
target_include_directories(chiaki-stream PRIVATE ${CMAKE_CURRENT_SOURCE_DIR} ${JUKAHUB_FFMPEG_INCLUDE})
target_link_libraries(chiaki-stream PRIVATE chiaki-lib ${JUKAHUB_FFMPEG_LINK} PkgConfig::TRIMUI_SDL2 ${JUKAHUB_SYSTEM_LIBS})

add_executable(chiaki-regist chiaki-regist.c)
target_compile_features(chiaki-regist PRIVATE c_std_11)
target_compile_options(chiaki-regist PRIVATE -Wall -Wextra -Wpedantic)
target_link_libraries(chiaki-regist PRIVATE chiaki-lib ${JUKAHUB_SYSTEM_LIBS})

# build-helpers.sh copies both binaries from this directory, so it is set here
# rather than left to whatever CMAKE_RUNTIME_OUTPUT_DIRECTORY chiaki-ng's own
# build may define.
#
# C_EXTENSIONS keeps the GNU dialect (-std=gnu11) whatever chiaki-ng sets: under
# a strict -std=c11 glibc maps signal() to the one-shot System V flavour, and a
# second SIGTERM would then kill the stream before it writes its status file.
set_target_properties(chiaki-stream chiaki-regist PROPERTIES
  RUNTIME_OUTPUT_DIRECTORY "${CMAKE_CURRENT_BINARY_DIR}"
  C_STANDARD 11
  C_STANDARD_REQUIRED ON
  C_EXTENSIONS ON)

install(TARGETS chiaki-stream chiaki-regist RUNTIME DESTINATION bin)
