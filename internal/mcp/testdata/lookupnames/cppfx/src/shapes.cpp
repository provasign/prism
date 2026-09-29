#include "shapes.hpp"
namespace geo {
namespace detail { int clamp(int v) { return v < 0 ? 0 : v; } }
Circle::Circle(double r) : r_(r) {}
double Circle::area() const { return 3.14159 * r_ * r_; }
void Circle::scale(double f) { r_ *= f; }
void Circle::scale(double fx, double fy) { r_ *= (fx + fy) / 2; }
const std::string& Shape::name_() const { return name; }
template <typename T> T Box<T>::get() const { return value; }
double total_area(const Shape* const* s, int n) { double t = 0; for (int i = 0; i < n; i++) t += s[i]->area(); return t; }
}
int helper(int x) { return x; }
