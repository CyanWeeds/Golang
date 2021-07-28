package main

import "fmt"

// 声明变量
/*
var name string
var age int
var isOK bool*/
// 批量声明
var (
	name string
	age  int
	// isOK bool
	// 全局变量声明后可以不使用
)

// 赋值
func main() {
	name = "张三"
	age = 16
	// isOK = true
	// 非全局变量声明后必须使用

	// 打印
	// fmt.print(isOK)             // 在终端中输出
	fmt.Printf("name:%s\n", name) // %s:占位符
	fmt.Println(age)              // 打印完会在后面加一个换行符

	// 声明变量同时赋值
	var s1 string = "黑客"
	fmt.Println(s1)
	// 根据值判断变量类型
	var s2 = "20"
	fmt.Println(s2)
	// 简短变量声明，只能在函数里用
	s3 := "吃完"
	fmt.Println(s3)

	// 匿名变量:_
}
