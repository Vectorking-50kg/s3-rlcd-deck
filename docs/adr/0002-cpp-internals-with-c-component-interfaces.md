# Use C++ internals behind C-compatible component interfaces

Implement firmware component internals in C++17 where resource ownership and state benefit from stronger lifetime management, while exposing narrow C-compatible interfaces at component seams. This keeps ESP-IDF and LVGL integration straightforward, permits hardware-independent host tests behind adapters, and avoids spreading C++ object ownership across component boundaries.
