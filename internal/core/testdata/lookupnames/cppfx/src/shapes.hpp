#pragma once
#include <string>
namespace geo {
namespace detail { int clamp(int v); }
class Shape {
public:
    virtual ~Shape() = default;
    virtual double area() const = 0;
    std::string name;
    const std::string& name_() const;
};
class Circle : public Shape {
public:
    explicit Circle(double r);
    double area() const override;
    double radius() const { return r_; }
    void scale(double f);
    void scale(double fx, double fy);
private:
    double r_;
};
template <typename T> class Box { public: T get() const; T value; };
struct Rect { double w, h; double area() const { return w*h; } };
double total_area(const Shape* const* s, int n);
}
