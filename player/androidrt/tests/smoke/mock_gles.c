/* mock_gles.c — a fake glibc libGLESv2 / libGLESv1_CM for the smoke test of
 * the present layer's GLES binding: eglpresent.c binds the game's
 * libGLESv2.so (and libGLESv3.so, libGLESv1_CM.so) to the library beside the
 * chosen libEGL, not to whatever the library path finds first. run-smoke.sh
 * builds one copy beside the mock libEGL and a decoy on LD_LIBRARY_PATH;
 * MOCKGLES_ID names the copy in glGetString(GL_VERSION), so the game's own GL
 * call says which one its import reached.
 */
#ifndef MOCKGLES_ID
#define MOCKGLES_ID "unnamed"
#endif

#define GL_VERSION 0x1F02

__attribute__((visibility("default"))) const unsigned char *glGetString(unsigned name) {
    return (const unsigned char *)(name == GL_VERSION ? "OpenGL ES 3.2 mockgles " MOCKGLES_ID : "mockgles");
}
