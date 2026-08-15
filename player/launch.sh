#!/bin/sh

# Set the working directory
cd "$(dirname "$0")"

### THIS IS NEEDED TO GET NATIVE VERSION
# Copy LibSDL files only if they don't exist
if [ ! -f libSDL2.so ]; then
    cp /usr/trimui/lib/libSDL2-2.0.so.0 .
    mv libSDL2-2.0.so.0 libSDL2.so
    echo "libSDL2 copied."
fi

if [ ! -f libSDL2_ttf.so ]; then
    cp /usr/trimui/lib/libSDL2_ttf-2.0.so.0 .
    mv libSDL2_ttf-2.0.so.0 libSDL2_ttf.so
    echo "libSDL2_ttf copied."
fi

if [ ! -f libSDL2_image.so ]; then
    cp /usr/trimui/lib/libSDL2_image-2.0.so.0 .
    mv libSDL2_image-2.0.so.0 libSDL2_image.so
    echo "libSDL2_image copied."
fi

if [ ! -f libSDL2_mixer.so ]; then
    cp /usr/trimui/lib/libSDL2_mixer-2.0.so.0 .
    mv libSDL2_mixer-2.0.so.0 libSDL2_mixer.so
    echo "libSDL2_mixer copied."
fi
####

export LD_LIBRARY_PATH="$(dirname "$0"):/lib64:/usr/trimui/lib:/usr/lib:/usr/trimui/lib:$LD_LIBRARY_PATH"
export CLR_OPENSSL_VERSION_OVERRIDE=1.1

./JukaHub &> errors.txt

exit 0
