package review

import "github.com/Sibuxiangx/loveace-go/core/services/jwc"

// Free-classroom answers use invented rooms in an invented building, so the
// review account never shows a real campus layout or a real occupancy.

func classroomBuildings() []jwc.FreeClassroomBuilding {
	return []jwc.FreeClassroomBuilding{
		{CampusNumber: "2", CampusName: "西校", BuildingNumber: "36", BuildingName: "示例教学楼"},
		{CampusNumber: "1", CampusName: "东校", BuildingNumber: "12", BuildingName: "示例东楼", Stopped: true},
	}
}

func classroomMeta() jwc.FreeClassroomMeta {
	return jwc.FreeClassroomMeta{
		CurrentWeek: new(int64(8)),
		Types:       []jwc.FreeClassroomOption{{Code: "32", Name: "示例普通教室"}},
	}
}

func classroomRooms() []jwc.FreeClassroom {
	return []jwc.FreeClassroom{
		{
			CampusNumber: "2", CampusName: "西校", BuildingNumber: "36", BuildingName: "示例教学楼",
			ClassroomNumber: "review-101", ClassroomName: "示例教室101", StatusCode: "01",
			TypeCode: "32", TypeName: "示例普通教室", Seats: new(int64(60)),
		},
		{
			CampusNumber: "2", CampusName: "西校", BuildingNumber: "36", BuildingName: "示例教学楼",
			ClassroomNumber: "review-102", ClassroomName: "示例教室102", StatusCode: "01",
			TypeCode: "32", TypeName: "示例普通教室", Seats: new(int64(48)),
		},
	}
}

func classroomOccupancy() []string {
	return []string{"2_1_7", "2_2_7"}
}
